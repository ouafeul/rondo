# The page image: the component CI builds it and pushes it with the appVersion tag
# of apps/__NAME__-web (rondo-sandbox.yaml: images, tagFromChart).
FROM busybox:1.37.0
COPY start.sh /start.sh
USER 65534
EXPOSE 8080
ENTRYPOINT ["/bin/sh", "/start.sh"]
