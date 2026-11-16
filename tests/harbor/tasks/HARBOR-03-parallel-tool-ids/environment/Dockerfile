# Pinned environment stub for HARBOR-03
FROM python:3.11.13-slim-bookworm
WORKDIR /app
COPY --chown=10001:10001 workspace/ /app/
USER 10001:10001
CMD ["python3", "-c", "print('harbor-03 env stub')"]
