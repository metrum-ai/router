# Pinned environment stub for HARBOR-04
FROM python:3.11.13-slim-bookworm
RUN useradd --uid 10001 --user-group --create-home --home-dir /home/harbor --shell /usr/sbin/nologin harbor \
    && install -d -o 10001 -g 10001 /app /logs/verifier
ENV HOME=/home/harbor
WORKDIR /app
COPY --chown=10001:10001 workspace/ /app/
USER 10001:10001
CMD ["python3", "-c", "print('harbor-04 env stub')"]
