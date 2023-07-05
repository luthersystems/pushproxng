FROM golang:alpine
COPY build/proxy /opt/
WORKDIR /opt
ENTRYPOINT [ "/opt/proxy" ]
CMD [ "--listen=:8080" ]
