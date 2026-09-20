# Pikachu router runner pool (manual ops)

Compose project on this host: `~/github-runners-router-compose`  
GitHub labels: `self-hosted`, `Linux`, `X64`, `router`  
Repo: https://github.com/metrum-ai/router

## Quick commands

```bash
cd ~/github-runners-router-compose

./manage.sh status      # compose ps + GitHub runner list
./manage.sh start       # up -d
./manage.sh stop        # down
./manage.sh restart     # force-recreate from current images
./manage.sh logs        # follow all container logs
./manage.sh logs runner-1
./manage.sh rebuild     # new registration token + image build + recreate
./manage.sh register-token
```

Equivalent raw compose (if you prefer):

```bash
cd ~/github-runners-router-compose
sudo docker-compose ps
sudo docker-compose up -d
sudo docker-compose down
sudo docker-compose up -d --force-recreate
sudo docker-compose logs -f --tail=100
sudo docker-compose build && sudo docker-compose up -d --force-recreate
```

## First-time / token

1. Ensure `.env` exists (`cp .env.example .env && chmod 600 .env`).
2. Set `RUNNER_TOKEN` (preferred) or `ACCESS_TOKEN`:

```bash
gh api -X POST repos/metrum-ai/router/actions/runners/registration-token --jq .token
# paste into .env as RUNNER_TOKEN=...
./manage.sh start
```

Tokens expire in about an hour. If containers loop on registration errors, run `./manage.sh rebuild`.

## Design notes

- **3 replicas** (`gha-router-1..3`) so concurrent CI jobs do not share one host `dpkg` lock.
- **Privileged** containers so nested `bubblewrap` user namespaces work for LRP.
- **AppArmor is not required**; LRP CI uses bubblewrap namespaces only.
- **Ephemeral runners**: one job per registration life; compose `restart: unless-stopped` brings a fresh listener back.

## Do not

- Do not start the old bare-metal `~/github-runners-router` listener again with the same labels (jobs will split / fight).
- Leave `~/github-runners-bench-cli` and `~/github-runners-all-smi` alone; they are separate pools.
