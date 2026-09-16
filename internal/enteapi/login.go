package enteapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"

	"github.com/kong/go-srp"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/encoding"
)

// srpGroup is the SRP-6a group size Ente uses. Both sides must agree, so this
// is fixed, not configurable.
const srpGroup = 4096

// SRPAttributes are the per-account key-derivation and SRP parameters, fetched
// before login so the client can derive the same login key the server holds a
// verifier for.
type SRPAttributes struct {
	SRPUserID string `json:"srpUserID"`
	SRPSalt   string `json:"srpSalt"`
	// MemLimit and OpsLimit are this account's Argon2id cost parameters.
	// They vary by account age and must be used as given: hardcoding
	// defaults derives the wrong key and login fails with no useful
	// message. MemLimit is in bytes and can be as large as 1 GiB.
	MemLimit int    `json:"memLimit"`
	OpsLimit int    `json:"opsLimit"`
	KekSalt  string `json:"kekSalt"`
	// IsEmailMFAEnabled means the account requires an emailed code instead
	// of the password/SRP exchange.
	IsEmailMFAEnabled bool `json:"isEmailMFAEnabled"`
}

// KeyAttributes carries the user's encrypted key material. Everything here is
// opaque to the server; only the password unlocks it.
type KeyAttributes struct {
	KEKSalt                  string `json:"kekSalt"`
	EncryptedKey             string `json:"encryptedKey"`
	KeyDecryptionNonce       string `json:"keyDecryptionNonce"`
	PublicKey                string `json:"publicKey"`
	EncryptedSecretKey       string `json:"encryptedSecretKey"`
	SecretKeyDecryptionNonce string `json:"secretKeyDecryptionNonce"`
	MemLimit                 int    `json:"memLimit"`
	OpsLimit                 int    `json:"opsLimit"`
}

// AuthorizationResponse is returned by every step of the login ladder. Which
// fields are populated tells you whether another step is required.
type AuthorizationResponse struct {
	ID             int64          `json:"id"`
	KeyAttributes  *KeyAttributes `json:"keyAttributes,omitempty"`
	EncryptedToken string         `json:"encryptedToken,omitempty"`
	AccountsURL    string         `json:"accountsUrl"`
	Token          string         `json:"token,omitempty"`

	TwoFactorSessionID string `json:"twoFactorSessionID"`
	PasskeySessionID   string `json:"passkeySessionID"`

	// SrpM2 is the server's proof that it holds the SRP verifier. Checking
	// it is what stops an impostor server from harvesting a login.
	SrpM2 *string `json:"srpM2,omitempty"`
}

func (a *AuthorizationResponse) totpRequired() bool    { return a.TwoFactorSessionID != "" }
func (a *AuthorizationResponse) passkeyRequired() bool { return a.PasskeySessionID != "" }

// Credentials is everything a successful login yields: the API token plus the
// key material needed to decrypt albums and files.
//
// Every field is secret. Treat this struct as sensitive: it is what
// internal/session encrypts at rest.
type Credentials struct {
	UserID int64
	Email  string

	// Token is the raw decrypted token. Use TokenHeader for the header value.
	Token []byte

	// MasterKey unwraps the keys of collections the user owns.
	MasterKey []byte
	// SecretKey and PublicKey form the user's X25519 pair, used to unwrap
	// the keys of collections shared with them by others.
	SecretKey []byte
	PublicKey []byte
}

// TokenHeader returns the token encoded the way museum expects to receive it
// in X-Auth-Token: base64url, not standard base64.
func (c *Credentials) TokenHeader() string {
	return base64.URLEncoding.EncodeToString(c.Token)
}

// Prompter supplies the interactive parts of login. It is an interface so the
// flow can be driven by something other than a terminal, and so nothing here
// reaches for global state.
type Prompter interface {
	// Password reads a secret without echoing it.
	Password(label string) (string, error)
	// Code reads a short numeric code, such as a TOTP or emailed OTP.
	Code(label string) (string, error)
	// Notify reports progress or instructions to the user.
	Notify(format string, args ...any)
	// WaitForEnter blocks until the user acknowledges, used for the
	// passkey flow where verification happens in a browser.
	WaitForEnter(prompt string) error
}

// ErrLoginIncomplete means the server finished the flow without handing over
// the key material we need. In practice this is an account that has never
// completed setup in a first-party client.
var ErrLoginIncomplete = errors.New("login completed but the account has no key attributes or token; finish setting the account up in the Ente app first")

// Login performs the full interactive login ladder for email and returns the
// resulting credentials.
//
// The ladder mirrors Ente's own clients: SRP with the password (or an emailed
// code if the account is configured for that), then TOTP, then passkey, in
// whichever combination the server demands.
func Login(ctx context.Context, c *Client, email string, p Prompter) (*Credentials, error) {
	srpAttr, err := c.srpAttributes(ctx, email)
	useEmailCode := false
	if err != nil {
		// 404 here is not a failure: it is how the server says this
		// account has no SRP verifier and must use an emailed code.
		if StatusCode(err) == 404 {
			useEmailCode = true
		} else {
			return nil, err
		}
	} else if srpAttr.IsEmailMFAEnabled {
		useEmailCode = true
	}

	var (
		auth *AuthorizationResponse
		// kek is the password-derived key encryption key. SRP login
		// already computed it, so carrying it forward avoids a second
		// password prompt (and a second multi-second Argon2 run).
		kek []byte
	)
	if useEmailCode {
		auth, err = c.loginViaEmailCode(ctx, email, p)
	} else {
		auth, kek, err = c.loginViaPassword(ctx, srpAttr, p)
	}
	if err != nil {
		return nil, err
	}

	if auth.totpRequired() {
		if auth, err = c.verifyTOTP(ctx, auth, p); err != nil {
			return nil, err
		}
	}
	if auth.passkeyRequired() {
		if auth, err = c.verifyPasskey(ctx, auth, p); err != nil {
			return nil, err
		}
	}

	if auth.EncryptedToken == "" || auth.KeyAttributes == nil {
		return nil, ErrLoginIncomplete
	}
	return unlock(auth, kek, email, p)
}

func (c *Client) srpAttributes(ctx context.Context, email string) (*SRPAttributes, error) {
	var res struct {
		Attributes *SRPAttributes `json:"attributes"`
	}
	if err := c.Get(ctx, "/users/srp/attributes", url.Values{"email": {email}}, &res); err != nil {
		return nil, err
	}
	if res.Attributes == nil {
		return nil, errors.New("server returned no SRP attributes")
	}
	return res.Attributes, nil
}

// loginViaPassword runs the SRP-6a exchange, reprompting on a bad password.
// It returns the key encryption key alongside the response so the caller can
// reuse it for decryption.
func (c *Client) loginViaPassword(ctx context.Context, attr *SRPAttributes, p Prompter) (*AuthorizationResponse, []byte, error) {
	for {
		password, err := p.Password("Ente password")
		if err != nil {
			return nil, nil, err
		}

		p.Notify("Deriving key (this takes a few seconds)...")
		kek, err := crypto.DeriveArgonKey(password, attr.KekSalt, attr.MemLimit, attr.OpsLimit)
		if err != nil {
			return nil, nil, fmt.Errorf("deriving key encryption key: %w", err)
		}
		loginKey := crypto.DeriveLoginKey(kek)

		params := srp.GetParams(srpGroup)
		client := srp.NewClient(
			params,
			encoding.DecodeBase64(attr.SRPSalt),
			[]byte(attr.SRPUserID),
			loginKey,
			srp.GenKey(),
		)

		session, err := c.createSRPSession(ctx, attr.SRPUserID, encoding.EncodeBase64(client.ComputeA()))
		if err != nil {
			return nil, nil, err
		}
		client.SetB(encoding.DecodeBase64(session.SRPB))

		auth, err := c.verifySRPSession(ctx, attr.SRPUserID, session.SessionID, encoding.EncodeBase64(client.ComputeM1()))
		if err != nil {
			// A 401 is very probably a wrong password, which is worth
			// another try. Anything else is a real problem.
			if StatusCode(err) == 401 {
				p.Notify("That password was not accepted; try again.")
				continue
			}
			return nil, nil, err
		}

		// Verify the server's proof before trusting the token it just
		// handed us. Ente's own CLI skips this; without it a server
		// that does not actually know the verifier can still complete
		// a login. If the account predates srpM2 the field is absent,
		// in which case there is nothing to check.
		if auth.SrpM2 != nil {
			if err := client.CheckM2(encoding.DecodeBase64(*auth.SrpM2)); err != nil {
				return nil, nil, fmt.Errorf("server failed SRP proof (M2) check, refusing to continue: %w", err)
			}
		}
		return auth, kek, nil
	}
}

func (c *Client) loginViaEmailCode(ctx context.Context, email string, p Prompter) (*AuthorizationResponse, error) {
	if err := c.Post(ctx, "/users/ott", map[string]any{
		"email":   email,
		"purpose": "login",
	}, nil); err != nil {
		return nil, fmt.Errorf("requesting login code: %w", err)
	}
	p.Notify("A sign-in code has been emailed to %s.", email)

	for {
		code, err := p.Code("Emailed code")
		if err != nil {
			return nil, err
		}
		var auth AuthorizationResponse
		err = c.Post(ctx, "/users/verify-email", map[string]any{
			"email": email,
			"ott":   code,
		}, &auth)
		if err == nil {
			return &auth, nil
		}
		if StatusCode(err) == 401 || StatusCode(err) == 410 {
			p.Notify("That code was not accepted; try again.")
			continue
		}
		return nil, err
	}
}

type srpSession struct {
	SessionID string `json:"sessionID"`
	SRPB      string `json:"srpB"`
}

func (c *Client) createSRPSession(ctx context.Context, srpUserID, clientA string) (*srpSession, error) {
	var res srpSession
	if err := c.Post(ctx, "/users/srp/create-session", map[string]any{
		"srpUserID": srpUserID,
		"srpA":      clientA,
	}, &res); err != nil {
		return nil, fmt.Errorf("creating SRP session: %w", err)
	}
	return &res, nil
}

func (c *Client) verifySRPSession(ctx context.Context, srpUserID, sessionID, clientM1 string) (*AuthorizationResponse, error) {
	var res AuthorizationResponse
	if err := c.Post(ctx, "/users/srp/verify-session", map[string]any{
		"srpUserID": srpUserID,
		"sessionID": sessionID,
		"srpM1":     clientM1,
	}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) verifyTOTP(ctx context.Context, auth *AuthorizationResponse, p Prompter) (*AuthorizationResponse, error) {
	for {
		code, err := p.Code("Two-factor code")
		if err != nil {
			return nil, err
		}
		var res AuthorizationResponse
		err = c.Post(ctx, "/users/two-factor/verify", map[string]any{
			"sessionID": auth.TwoFactorSessionID,
			"code":      code,
		}, &res)
		if err == nil {
			return &res, nil
		}
		if StatusCode(err) == 401 || StatusCode(err) == 404 {
			p.Notify("That code was not accepted; try again.")
			continue
		}
		return nil, err
	}
}

// verifyPasskey cannot be completed in the terminal: WebAuthn needs a browser.
// We print the URL, wait, then ask the server whether verification landed.
func (c *Client) verifyPasskey(ctx context.Context, auth *AuthorizationResponse, p Prompter) (*AuthorizationResponse, error) {
	if auth.AccountsURL == "" {
		return nil, errors.New("server requires passkey verification but sent no accounts URL")
	}
	verifyURL := fmt.Sprintf(
		"%s/passkeys/verify?passkeySessionID=%s&redirect=%s&clientPackage=%s",
		auth.AccountsURL,
		url.QueryEscape(auth.PasskeySessionID),
		url.QueryEscape("ente-cli://passkey"),
		url.QueryEscape(clientPackage),
	)
	p.Notify("This account uses a passkey. Open this URL in a browser and complete verification:\n\n  %s\n", verifyURL)

	for {
		if err := p.WaitForEnter("Press Enter once the browser says verification is complete"); err != nil {
			return nil, err
		}
		var res AuthorizationResponse
		err := c.Get(ctx, "/users/two-factor/passkeys/get-token",
			url.Values{"sessionID": {auth.PasskeySessionID}}, &res)
		if err == nil {
			return &res, nil
		}
		if StatusCode(err) == 404 || StatusCode(err) == 401 {
			p.Notify("The server has not recorded a completed verification yet.")
			continue
		}
		return nil, err
	}
}

// unlock turns the server's encrypted key material into usable keys.
//
// The chain is: password -> Argon2id -> key encryption key -> master key ->
// secret key -> API token. kek may be nil when login went via an emailed code,
// in which case the password is still needed and must be prompted for.
func unlock(auth *AuthorizationResponse, kek []byte, email string, p Prompter) (*Credentials, error) {
	attr := auth.KeyAttributes
	publicKey := encoding.DecodeBase64(attr.PublicKey)

	// A key handed to us by loginViaPassword has already been proven
	// correct by the SRP exchange, so if it fails to decrypt anything the
	// problem is not a typo and reprompting would only mask it.
	kekWasProvenBySRP := kek != nil

	for {
		if kek == nil {
			password, err := p.Password("Ente password")
			if err != nil {
				return nil, err
			}
			p.Notify("Deriving key (this takes a few seconds)...")
			// Use the key attributes' own cost parameters here, not
			// the SRP attributes': they are separate fields and can
			// legitimately differ.
			kek, err = crypto.DeriveArgonKey(password, attr.KEKSalt, attr.MemLimit, attr.OpsLimit)
			if err != nil {
				return nil, fmt.Errorf("deriving key encryption key: %w", err)
			}
		}

		masterKey, err := crypto.SecretBoxOpen(
			encoding.DecodeBase64(attr.EncryptedKey),
			encoding.DecodeBase64(attr.KeyDecryptionNonce),
			kek,
		)
		if err != nil {
			if kekWasProvenBySRP {
				return nil, fmt.Errorf("password was verified over SRP but the master key would not decrypt: %w", err)
			}
			p.Notify("Incorrect password; try again.")
			kek = nil
			continue
		}

		secretKey, err := crypto.SecretBoxOpen(
			encoding.DecodeBase64(attr.EncryptedSecretKey),
			encoding.DecodeBase64(attr.SecretKeyDecryptionNonce),
			masterKey,
		)
		if err != nil {
			return nil, fmt.Errorf("decrypting account secret key: %w", err)
		}

		token, err := crypto.SealedBoxOpen(
			encoding.DecodeBase64(auth.EncryptedToken),
			publicKey,
			secretKey,
		)
		if err != nil {
			return nil, fmt.Errorf("decrypting API token: %w", err)
		}

		return &Credentials{
			UserID:    auth.ID,
			Email:     email,
			Token:     token,
			MasterKey: masterKey,
			SecretKey: secretKey,
			PublicKey: publicKey,
		}, nil
	}
}

// UserDetails is the subset of /users/details/v2 we use, which is purely to
// confirm a session works and belongs to who we think it does.
type UserDetails struct {
	Email string `json:"email"`
}

// FetchUserDetails verifies the current token by asking who it belongs to.
func (c *Client) FetchUserDetails(ctx context.Context) (*UserDetails, error) {
	var res UserDetails
	if err := c.Get(ctx, "/users/details/v2", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
