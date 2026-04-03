# Copyright 2025 Gluesys FlexA Inc.

FROM alpine:latest

WORKDIR /root

COPY ./bin/flexa-csi-driver /bin/flexa-csi-driver

RUN chmod +x /bin/flexa-csi-driver

ENTRYPOINT ["/bin/flexa-csi-driver"]
