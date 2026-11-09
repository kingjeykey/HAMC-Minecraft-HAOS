# HAMC Server (Java)

This add-on allows for hosting a Minecraft Java server using Home Assistant. Based on the Minecraft server docker by [itzg](https://github.com/itzg/docker-minecraft-server).

## Configuration

All configuration is done using the add-on options. It allows for setting the environment variables found [here](https://github.com/itzg/docker-minecraft-server).

To access the Minecraft server from outside your network, forward port 25565 (TCP) on your router. Then connect using:

```
<your_ip>:25565
```

## References

* Thanks to [alexbelgium](https://github.com/alexbelgium/hassio-addons) for the add-on template.
* Thanks to [itzg](https://github.com/itzg/docker-minecraft-server) for the docker image.
* Original add-on by [williamcorsel](https://github.com/williamcorsel/hassio-addons), now maintained by [kingjeykey](https://github.com/kingjeykey/HAMC-Minecraft-HAOS).

## Java version

The Java version is set by the base image tag in `build.yaml` (amd64/aarch64: Java 25). Minecraft 26.1 and newer needs Java 25, 1.20.5 to 1.21.x needs Java 21, 1.18 to 1.20.4 needs Java 17. Older versions also run on Java 25, but some mods require an older Java.

## Web UI (sidebar "Minecraft")

After starting the add-on a **Minecraft** entry appears in the Home Assistant sidebar (or use *Open Web UI*). It works with every server type.

| Tab | What it does |
| --- | --- |
| Overview | Online state, players, tick time (MSPT) or TPS, CPU/RAM of the add-on, free disk space, history graphs |
| Console | Live server log and command input |
| Whitelist | Add/remove players and switch the whitelist on/off. Changes are also written into the `WHITELIST` option |
| Files | Browse the server folder, upload plugins/mods, download files or whole worlds as ZIP, edit text files, unzip, rename, delete, load a file from a URL |

Notes:

- Console input, TPS values and whitelist changes use RCON. The add-on generates an RCON password automatically if `RCON_PASSWORD` is empty. TPS/MSPT are read with the commands the server offers (`tick query`, `tps`, `mspt`, `forge tps`, ...). If a server type has none of them, the values show as unavailable.
- Large uploads through the Home Assistant frontend can be slow. For big files use **Von Adresse laden** (the server downloads the file itself) or the Samba/SSH add-on.
- Changes to `server.properties` are overwritten by the add-on options on every start. Use the options for settings that exist there.
- Port 25575 (RCON) is only needed for external tools. The web UI does not need it, so you can disable it under *Network* in the add-on.

## Home Assistant entities (MQTT)

With the Mosquitto broker add-on and the MQTT integration, the device **HAMC Minecraft Server** is created automatically with these entities: online, players online/max, latency, TPS, MSPT (average and 95th percentile), add-on CPU, RAM, free disk space, whitelist (active, entries), version and MOTD. Without Mosquitto, enter your broker in `MQTT_HOST`, `MQTT_PORT`, `MQTT_USER` and `MQTT_PASSWORD`. Set `MQTT_ENABLED` to `false` to switch it off.

## Tunnel without port forwarding (playit.gg)

If your provider uses CGNAT / DS-Lite, port forwarding does not work. The add-on can start a playit.gg agent instead:

1. Create a free account on https://playit.gg and generate an agent secret key (Docker setup).
2. In the add-on configuration set `PLAYIT_ENABLED` to `true` and paste the key into `PLAYIT_SECRET`.
3. On playit.gg create a tunnel of type *Minecraft Java* for this agent with local address `127.0.0.1` and port `25565`.
4. Start the add-on and share the address that playit.gg shows you with your players.

The server is then reachable from the internet. Use `WHITELIST` and keep `ONLINE_MODE` enabled.
The agent version is pinned in the `Dockerfile` (`PLAYIT_VERSION`) and has to be bumped when playit deprecates it.
