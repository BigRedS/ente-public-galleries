# Packages an already-build static site (the contents of ./out, as generated
# with `./ente-public-galleries build` for serving on k8s or docker or somesuch
#
# Intentionally doesn't just `build` in here to avoid dragging in Ente session
# credentials. It's a static site of flat files, it should just be a copy.
FROM nginx:alpine
COPY out/ /usr/share/nginx/html/
