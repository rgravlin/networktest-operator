FROM alpine/curl

# install dig
RUN apk add --update bind-tools

# install curl wrapper
COPY curl-wrapper.sh /bin/curl-wrapper.sh
