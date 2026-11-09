#!/usr/bin/env bashio

bashio::log.info "Starting..."

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
