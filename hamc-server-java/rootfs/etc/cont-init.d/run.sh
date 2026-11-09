#!/usr/bin/env bashio

bashio::log.info "Starting..."

# RCON is needed by the web UI (console, TPS, whitelist). Without a password in the
# options, generate one once and keep it, so RCON never runs with an empty password.
if ! bashio::config.has_value 'RCON_PASSWORD'; then
    if [ ! -s /hassio_data/rcon_password ]; then
        head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24 > /hassio_data/rcon_password
        chmod 600 /hassio_data/rcon_password
    fi
    export RCON_PASSWORD="$(cat /hassio_data/rcon_password)"
fi

# Web UI for the Home Assistant sidebar (restarts itself if it ever stops)
bashio::log.info "Starting web UI..."
( while true; do /usr/local/bin/hamc-ui; sleep 5; done ) &

# Optional playit.gg tunnel (no port forwarding needed)
if bashio::config.true 'PLAYIT_ENABLED'; then
    if bashio::config.has_value 'PLAYIT_SECRET'; then
        bashio::log.info "Starting playit.gg tunnel agent..."
        /usr/local/bin/playit --secret "$(bashio::config 'PLAYIT_SECRET')" --platform-docker &
    else
        bashio::log.warning "PLAYIT_ENABLED is true, but PLAYIT_SECRET is empty. Tunnel not started."
    fi
fi

/start
