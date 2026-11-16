# Pinned environment stub for HARBOR-01
# Offline verifier-integrity tests do not build this image.
FROM python:3.11.13-slim-bookworm
WORKDIR /app
COPY --chown=10001:10001 workspace/ /app/
USER 10001:10001
CMD ["python3", "-c", "print('harbor-01 env stub')"]
