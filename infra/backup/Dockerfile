FROM alpine:3.22
ENV RESTIC_CACHE_DIR=/tmp/restic-cache
RUN apk add --no-cache restic rclone redis postgresql17-client supercronic tzdata
COPY backup.sh restore-test.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/backup.sh /usr/local/bin/restore-test.sh \
 && printf '15 3 * * * /usr/local/bin/backup.sh\n' > /etc/crontab
USER nobody
CMD ["supercronic", "/etc/crontab"]
