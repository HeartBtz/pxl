#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

usage() {
	cat <<'EOF'
Usage: ./install.sh [--check]

Creates .env with locally generated secrets when it does not exist, validates
the Docker Compose configuration, then builds and starts PXL. Use --check to
validate prerequisites and configuration without starting containers.

This script does not install Docker, alter the host, or update an existing
.env file. Docker Engine, the Compose plugin, and OpenSSL are prerequisites.
EOF
}

check_only=false
case "${1:-}" in
"") ;;
--check) check_only=true ;;
-h | --help)
	usage
	exit 0
	;;
*)
	usage >&2
	exit 2
	;;
esac

command -v docker >/dev/null 2>&1 || {
	printf '%s\n' 'Docker is required.' >&2
	exit 1
}
docker compose version >/dev/null 2>&1 || {
	printf '%s\n' 'The Docker Compose plugin is required.' >&2
	exit 1
}

if [[ ! -e .env ]]; then
	command -v openssl >/dev/null 2>&1 || {
		printf '%s\n' 'OpenSSL is required to generate secrets.' >&2
		exit 1
	}
	umask 077
	cp .env.example .env
	db_secret="$(openssl rand -hex 24)"
	jwt_secret="$(openssl rand -hex 32)"
	admin_password="$(openssl rand -hex 16)"
	sed -i \
		-e "s/^PXL_DB_PASS=$/PXL_DB_PASS=${db_secret}/" \
		-e "s/^PXL_JWT_SECRET=$/PXL_JWT_SECRET=${jwt_secret}/" \
		-e "s/^PXL_ADMIN_PASS=$/PXL_ADMIN_PASS=${admin_password}/" \
		.env
	chmod 600 .env
	printf '%s\n' 'Created .env with generated database, JWT, and admin secrets.'
fi

for variable in PXL_DB_PASS PXL_JWT_SECRET PXL_ADMIN_PASS; do
	if ! grep -Eq "^${variable}=.+$" .env; then
		printf 'Set %s in .env before installation.\n' "${variable}" >&2
		exit 1
	fi
done

docker compose config --quiet
if [[ "${check_only}" == true ]]; then
	printf '%s\n' 'Docker Compose configuration is valid.'
	exit 0
fi

docker compose up --build --detach
printf '%s\n' 'PXL is starting. Check it with: docker compose ps'
