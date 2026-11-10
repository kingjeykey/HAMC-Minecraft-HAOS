## next

## 1.6.1

- After uploading a `.mrpack` file the install dialog opens automatically.

## 1.6.0

- New: Modrinth modpacks (`.mrpack`). Upload the file (or load it from a URL) in the Files tab and click "Modpack installieren". The add-on downloads all server mods, checks their checksums, applies the pack's config files and can set `TYPE` and `VERSION` for you. Client-only mods are skipped. Downloads are only allowed from the hosts the .mrpack specification permits (Modrinth CDN, GitHub, GitLab).
- Added `NEOFORGE` to the selectable server types.

## 1.5.1

- Fix: RCON connections were closed by the server at random ("RCON nicht erreichbar: EOF"), so TPS/MSPT, console input and whitelist changes did not work. The RCON client now sends its packets one at a time, as the Minecraft server expects. TPS detection is also retried sooner after a connection error.

## 1.5.0

- New: web UI in the Home Assistant sidebar ("Minecraft"), independent of the server type (Vanilla, Paper, Forge, Fabric, ...):
  - Overview: online state, players, tick time (MSPT) / TPS, add-on CPU and RAM, free disk space and history graphs.
  - Console: live server log and command input (via RCON).
  - Whitelist: add/remove players, switch on/off. Changes are written back into the `WHITELIST` option of the add-on.
  - Files: browse the server folder, upload and download files, download worlds/folders as ZIP, edit text files, unzip, rename, delete, load files from a URL.
- New: Home Assistant entities via MQTT discovery (device "HAMC Minecraft Server"): online, players, max players, latency, TPS, MSPT, add-on CPU/RAM, free disk space, whitelist, version, MOTD. New options `MQTT_ENABLED`, `MQTT_HOST`, `MQTT_PORT`, `MQTT_USER`, `MQTT_PASSWORD` (by default the broker is found automatically).
- If `RCON_PASSWORD` is empty, a random password is now generated and stored, so RCON is never open without a password.

## 1.4.0

- New: optional playit.gg tunnel. Set `PLAYIT_ENABLED` to true and paste your agent secret into `PLAYIT_SECRET` to make the server reachable without port forwarding (useful behind CGNAT / DS-Lite). The agent is pinned to playit v1.2.0 and disabled by default.

## 1.3.0

- Fix: the server did not start with recent Minecraft versions (`UnsupportedClassVersionError`, class file version 69). The base image now uses the explicit tag `itzg/minecraft-server:java25` (Java 25, required by Minecraft 26.1 and newer) instead of `latest`. The new tag also forces a fresh pull of the base image instead of reusing an outdated local copy.
- Dropped armv7: the Java 21/25 images required by current Minecraft versions are only published for amd64 and arm64.

## 1.2.0

- Exposed the RCON port (25575) for remote console access. Ensure to set a strong `RCON_PASSWORD` in the configuration to use this feature.
- Mount the data folder to `/addon_config` for easier access to configuration & world files by @meritw
- Fix the Gamemode option not working by renaming it to Mode

## 1.1.1

- Add ability to select server type by @joelhaasnoot

## 1.1.0

- Fixed HAOS option loading

## 1.0.0

- Initial release
