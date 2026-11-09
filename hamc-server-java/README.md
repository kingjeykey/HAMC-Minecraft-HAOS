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

## Tunnel without port forwarding (playit.gg)

If your provider uses CGNAT / DS-Lite, port forwarding does not work. The add-on can start a playit.gg agent instead:

1. Create a free account on https://playit.gg and generate an agent secret key (Docker setup).
2. In the add-on configuration set `PLAYIT_ENABLED` to `true` and paste the key into `PLAYIT_SECRET`.
3. On playit.gg create a tunnel of type *Minecraft Java* for this agent with local address `127.0.0.1` and port `25565`.
4. Start the add-on and share the address that playit.gg shows you with your players.

The server is then reachable from the internet. Use `WHITELIST` and keep `ONLINE_MODE` enabled.
The agent version is pinned in the `Dockerfile` (`PLAYIT_VERSION`) and has to be bumped when playit deprecates it.
