<p align="center">
 <img height="372" alt="Logo" src="https://github.com/user-attachments/assets/2bc6af8e-9568-468d-962e-7f336c034213" />
</p>
<h1 align="center">Monochroma</h1>

Monochroma is a [Navidrome](https://www.navidrome.org) fork that adds **Tidal streaming** as a seamlessly integrated virtual library. Your local music and Tidal's catalog appear side-by-side in the same UI and respond to the same Subsonic API. _Almost_ any Subsonic client just works.

---
## Index:

- [Self-hosting Guide](#self-hosting)
- [How the Tidal Integration Works](https://github.com/monochroma-sp3/Monochroma/blob/master/IN%20DEPTH.md)
- [About AI usage in this project](https://github.com/monochroma-sp3/Monochroma#about-ai-in-this-project)
- [Contribution](https://github.com/monochroma-sp3/Monochroma#contribution)

---
# Self-Hosting

Monochroma is designed to run on a Linux VPS (Ubuntu/Debian) alongside its two companion services: `hifi-api` (Tidal audio, port 8001) and the playlist transferer (port 8080).

```bash
git clone https://github.com/monochroma-sp3/Monochroma.git
cd Monochroma
./setup.sh
```

`setup.sh` installs the required toolchains (Go, Node, npm/pnpm, Python), builds both web UIs (Navidrome's own admin UI at `/app` and the Feishin client at `/`) and then the server itself, sets up `hifi-api` and the playlist transferer (each in their own venv), walks you through the Tidal login for `hifi-api`, writes `navidrome.toml`/`.env.local` from the example templates, and, if you opt in, installs systemd services for all three components so the whole stack starts on boot. It's safe to re-run: existing pieces are detected and reused.

> **Building manually?** Don't use a bare `go build`: the server embeds `ui/build/*` at compile time and that directory isn't checked in, so a plain `go build` produces a binary whose `/app` returns *404 page not found*. Use `make build`, which builds the admin UI first, or run `setup.sh`.

Once running, open `http://localhost:4533` for the Feishin UI (`/app` for Navidrome's own admin UI). Put it behind a reverse proxy (nginx/Caddy) with TLS for anything beyond local use, and keep `hifi-api`'s port firewalled to localhost; only Navidrome needs to reach it.

---
# About AI in this project
I want to be very transparent on this topic:<br>
This project and platform was developed using **Claude Opus and Fable.**<br>
Although this is not your typical "vibecoded" skid project. I understand the structure of the server and the Tidal integration, while AI has covered more in depth, technical functions.<br>
As a matter of fact, `IN DEPTH.md` was written with the help of claude, so that the AI can better explain to more intelligent people than me how the little technical details fit together.<br><br>
**PRs made with AI can be accepted, as long as the user making the PR understands what they're doing and they correctly and fully test their changes.**
<br><br>



---
# Contribution

If you want to contribute to the project, you're welcome to **Star** the repository, **Contribute** to the codebase or, if you feel like it, **[Donate](https://ko-fi.com/monochromacc)**.<br>
IF you decide to publicly host your instance for others to use, you create an Issue starting with `[Instance]` and the link to your instance so it can be published in the repository.
