## next

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
