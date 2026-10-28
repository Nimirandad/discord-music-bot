# Oracle Cloud Infrastructure (OCI) Always Free

Terraform creates a public VCN/subnet, an Internet Gateway, a security list, and one Ubuntu 24.04 ARM64 compute instance. A cloud-init script installs Docker and prepares a systemd service for the bot image. The bot runs as one process, matching its in-memory per-server queues and voice sessions.

## Free Tier sizing and caveats

- The instance uses `VM.Standard.A1.Flex` with 2 OCPUs and 12 GB RAM, the published monthly Always Free A1 allowance when continuously allocated.
- The boot volume defaults to 50 GB. The Always Free block-volume allowance is shared across the tenancy (documented as 200 GB); existing volumes count against it.
- The VM is created in the tenancy's home region. Always Free capacity may be unavailable in that region, and Oracle may reclaim an instance that meets its published idle-instance criteria.
- These limits do not guarantee capacity or uninterrupted availability. Verify current Oracle pricing and your tenancy's usage before applying; resources beyond the remaining Always Free quotas can incur charges.
- No inbound application port is opened. SSH ingress is disabled unless a trusted CIDR is provided. Outbound traffic is unrestricted for Discord Gateway/voice, Spotify, YouTube, and container image pulls.

## Prerequisites

- Terraform 1.5 or later.
- OCI CLI/configuration file with permissions to create VCN, networking, and Compute resources in the compartment. The provider reads the `DEFAULT` profile from `~/.oci/config` unless configured otherwise.
- An OCI home-region name, compartment OCID, and SSH key pair.
- A **public ARM64 container image** built from the repository's Dockerfile and pushed to a registry. Do not pass registry credentials or bot secrets to Terraform.

## Build and publish the image

From the project root, build and push a versioned ARM64 image to a public registry, for example GitHub Container Registry:

```powershell
$image = "ghcr.io/<owner>/discord-music-bot:1.0.0"
docker buildx build --platform linux/arm64 --push -t $image .
```

The registry repository must be public so the VM can pull the image without storing credentials in Terraform or cloud-init. If you use another registry, substitute its public image reference.

## Provision

From this directory, set the tenancy's **home region**, target compartment, SSH public key, and the trusted address allowed to SSH. Use the public IPv4 address from your network with a `/32`; omit `ssh_ingress_cidr` to keep SSH closed (you will then need another access path before configuring the bot):

```powershell
terraform init
terraform plan -out=tfplan `
  -var="region=<home-region>" `
  -var="compartment_ocid=<compartment-ocid>" `
  -var="ssh_public_key=$(Get-Content $HOME\.ssh\id_ed25519.pub -Raw)" `
  -var="ssh_ingress_cidr=<your-public-ip>/32" `
  -var="container_image=$image"
terraform apply tfplan
```

If you do not already have an SSH key, generate one with `ssh-keygen -t ed25519`. Keep the private key local; provide only the `.pub` file. Terraform state and plan files contain infrastructure metadata and the public key, but no bot credentials. Keep them out of source control and use an encrypted, access-controlled backend for shared use.

The OCI provider uses the configured OCI CLI profile for authentication. Terraform outputs the public IP:

```powershell
$ip = terraform output -raw instance_public_ip
ssh ubuntu@$ip
```

Wait for cloud-init to finish before proceeding:

```bash
cloud-init status --wait
```

## Configure the bot secret and start it

On the VM, create the environment file as root with mode `0600`; enter the values in the editor rather than including secrets in a shell command or Terraform variable:

```bash
sudo install -m 600 /dev/null /etc/discord-music-bot.env
sudo nano /etc/discord-music-bot.env
```

Add `DISCORD_TOKEN=<token>`. Spotify is optional; when enabled, add both `SPOTIFY_CLIENT_ID=<id>` and `SPOTIFY_CLIENT_SECRET=<secret>`. Do not add unmatched Spotify variables. The service supplies `HTTP_ADDR=:8080` and `AUTO_LEAVE_SECONDS=180` by default; add `AUTO_LEAVE_SECONDS=<seconds>` to the environment file to override the latter. The app defaults to `SPOTIFY_REDIRECT_URI=http://127.0.0.1:18080/spotify/callback` and stores its refresh token at `/var/lib/discord-music-bot/spotify-token.json` in a persistent Docker volume.

### Authorize Spotify playlists

1. In Spotify Developer Dashboard, add the exact Redirect URI `http://127.0.0.1:18080/spotify/callback`. The app owner must have Premium, and the Spotify account used below must be in **User Management**.
2. From the computer where you will open the browser, start an SSH tunnel in a separate terminal:

   ```bash
   ssh -L 18080:127.0.0.1:8080 ubuntu@<instance-public-ip>
   ```

   Keep the tunnel open. It exposes the VM's loopback-only HTTP service to your local loopback; no public callback port is opened.

3. Open `http://127.0.0.1:18080/spotify/login` in that computer's browser and authorize with the allowlisted Premium account. A success page confirms that the refresh token was saved to the persistent Docker volume. Then try the public playlist link again.

The callback uses a one-time OAuth state value with a ten-minute lifetime. The refresh token is stored inside the container's persistent Docker volume, not in Terraform or the environment file. Do not copy or share the token file. If authorization is declined or expires, open the login URL again while the tunnel is active.

### Configure YouTube cookies for yt-dlp

YouTube OAuth login is not supported by yt-dlp. If yt-dlp asks you to sign in, you can optionally export a `cookies.txt` file from a browser session. Cookies are session credentials: never commit them, include them in an image, or paste them into chat. YouTube may expire or rotate them, and using an account with yt-dlp carries a risk of account restrictions.

The Oracle deployment also runs the pinned `bgutil-ytdlp-pot-provider` 2.0.2 service on a private Docker network. The bot image includes the matching yt-dlp plugin, `yt-dlp-ejs` challenge solvers, and Deno 2.3.7 runtime; it uses the `mweb` player to request PO Tokens. The provider port is not exposed publicly. PO Tokens do not guarantee that YouTube will allow a video or Oracle IP, so cookies may still be needed for login challenges.

After exporting cookies in Netscape format, transfer the file to the VM over SSH. The following commands assume the file is in your local Downloads folder; they do not print its contents:

```powershell
$ip = "132.226.156.155"
$cookieFile = "$HOME\Downloads\www.youtube.com_cookies.txt"
ssh ubuntu@$ip "install -d -m 700 ~/.discord-music-bot"
scp $cookieFile "ubuntu@${ip}:.discord-music-bot/youtube-cookies.txt"
ssh ubuntu@$ip "sudo docker volume inspect discord-music-bot-spotify-data --format '{{.Mountpoint}}'"
```

Use the mountpoint returned by the last command in place of `<volume-mountpoint>` below. Install the file as the container's non-root application user and set the environment variable without exposing the rest of the environment file:

```bash
sudo docker exec discord-music-bot id -u app
sudo docker exec discord-music-bot id -g app
sudo install -o <app-uid> -g <app-gid> -m 600 /home/ubuntu/.discord-music-bot/youtube-cookies.txt <volume-mountpoint>/youtube-cookies.txt
sudo rm -f /home/ubuntu/.discord-music-bot/youtube-cookies.txt
sudo sh -c 'if grep -q "^YTDLP_COOKIES_FILE=" /etc/discord-music-bot.env; then sed -i "s|^YTDLP_COOKIES_FILE=.*|YTDLP_COOKIES_FILE=/var/lib/discord-music-bot/youtube-cookies.txt|" /etc/discord-music-bot.env; else printf "\nYTDLP_COOKIES_FILE=/var/lib/discord-music-bot/youtube-cookies.txt\n" >> /etc/discord-music-bot.env; fi'
sudo systemctl restart discord-music-bot
```

The application reads the same mounted file for YouTube search and audio streaming. The cookies remain in the persistent Docker volume across container restarts. To remove them, delete the file from the volume and remove `YTDLP_COOKIES_FILE` from `/etc/discord-music-bot.env`, then restart the service. They can still expire or fail to bypass YouTube's restrictions; inspect the bot logs if playback continues to fail.

The PO Token provider is managed separately by `bgutil-ytdlp-pot-provider.service`; inspect or restart it with `sudo systemctl status bgutil-ytdlp-pot-provider` and `sudo systemctl restart bgutil-ytdlp-pot-provider`. The bot depends on this service and uses only the internal Docker network `discord-music-bot`.

Start the service and inspect status/logs:

```bash
sudo systemctl start discord-music-bot
sudo systemctl status discord-music-bot
sudo journalctl -u discord-music-bot -f
```

The service waits until `/etc/discord-music-bot.env` exists, pulls the configured image, and restarts the container after a failure or VM reboot. The HTTP health and OAuth endpoints are only bound to loopback on the VM and are not exposed publicly. Rotate credentials by editing the root-only environment file and restarting the service.

## Update the image

Push a new versioned ARM64 image, then on the VM update the image reference in `/etc/discord-music-bot-image` and restart:

```bash
sudo nano /etc/discord-music-bot-image
sudo systemctl restart discord-music-bot
```

Use the format `CONTAINER_IMAGE=ghcr.io/<owner>/discord-music-bot:<tag>`. The systemd service pulls the image before starting the container.

## Automatic GitHub Actions deployment over Tailscale

The repository workflow `.github/workflows/deploy.yml` runs tests and `go vet`, builds a Linux ARM64 image, publishes it to GHCR, then deploys it over a private Tailscale connection. The first workflow run publishes `1.1.5`; later runs increment the patch version. A failed health check restores the previous image reference and restarts the service. Each deployment restarts the bot, so its in-memory queues and active voice sessions end.

### Prepare the Oracle VM

Install Tailscale on the VM and authenticate it into your tailnet. In the Tailscale admin console, assign the VM the tag `tag:oracle-prod`. Keep TCP/22 closed to the public Internet in the OCI security list; the CI runner reaches SSH through the tailnet. Add a tailnet policy rule allowing `tag:ci` to reach `tag:oracle-prod` on TCP/22. Merge these entries into the existing policy rather than replacing unrelated rules:

```json
{
  "tagOwners": {
    "tag:ci": ["autogroup:admin"],
    "tag:oracle-prod": ["autogroup:admin"]
  },
  "acls": [
    { "action": "accept", "src": ["tag:ci"], "dst": ["tag:oracle-prod:22"] }
  ]
}
```

Install Tailscale using its [official Linux instructions](https://tailscale.com/kb/1031/install-linux/), authenticate the VM, and run `sudo tailscale up --advertise-tags=tag:oracle-prod`. Approve the device tag in the admin console if prompted.

Install the restricted deploy helper and account on the existing VM. Run these commands from the repository root on your workstation; replace the host with the VM's current public SSH address for this one-time setup:

```powershell
ssh-keygen -t ed25519 -f "$HOME\.ssh\discord-music-ci" -C "discord-music-bot GitHub Actions"
scp deployments/oracle/deploy-image.sh ubuntu@<oracle-public-ip>:/tmp/deploy-discord-music-bot
Get-Content "$HOME\.ssh\discord-music-ci.pub" | ssh ubuntu@<oracle-public-ip> "sudo useradd --create-home --shell /bin/bash discord-deploy 2>/dev/null || true; sudo install -d -o discord-deploy -g discord-deploy -m 700 /home/discord-deploy/.ssh; sudo tee /home/discord-deploy/.ssh/authorized_keys >/dev/null; sudo chown discord-deploy:discord-deploy /home/discord-deploy/.ssh/authorized_keys; sudo chmod 600 /home/discord-deploy/.ssh/authorized_keys; sudo install -o root -g root -m 755 /tmp/deploy-discord-music-bot /usr/local/sbin/deploy-discord-music-bot; echo 'discord-deploy ALL=(root) NOPASSWD: /usr/local/sbin/deploy-discord-music-bot' | sudo tee /etc/sudoers.d/discord-music-deploy >/dev/null; sudo chmod 440 /etc/sudoers.d/discord-music-deploy; sudo visudo -cf /etc/sudoers.d/discord-music-deploy; sudo rm -f /tmp/deploy-discord-music-bot"
```

Connect to the Oracle VM through Tailscale once and verify its MagicDNS name or `100.x` address. Obtain its SSH host key through that private connection and verify the fingerprint against `/etc/ssh/ssh_host_ed25519_key.pub` on the VM. This verified `known_hosts` entry will be stored in GitHub; do not trust an unverified `ssh-keyscan` result.

### Configure Tailscale and GitHub

1. In the Tailscale admin console, create an OAuth client with the `auth_keys` scope and permission to create ephemeral nodes tagged `tag:ci`. The workflow uses the official Tailscale GitHub Action to join the runner to the tailnet for the job, then removes that ephemeral runner afterward. See the [Tailscale GitHub Action setup](https://github.com/tailscale/github-action#prerequisites).
2. In GitHub, open **Settings → Secrets and variables → Actions** and add these repository secrets:

   | Secret | Value |
   |---|---|
   | `TS_OAUTH_CLIENT_ID` | Tailscale OAuth client ID |
   | `TS_OAUTH_SECRET` | Tailscale OAuth client secret |
   | `ORACLE_HOST` | `discord-deploy@<Oracle MagicDNS name or 100.x address>` |
   | `ORACLE_DEPLOY_SSH_KEY` | Contents of the private key `discord-music-ci`, including its header and footer |
   | `ORACLE_KNOWN_HOSTS` | Verified SSH host-key line for the exact hostname/IP in `ORACLE_HOST` |

3. Confirm `ghcr.io/nimirandad/discord-music-bot` is public and that GitHub Actions can publish packages. The workflow uses its short-lived `GITHUB_TOKEN`; no GHCR password is needed.
4. Commit and push the workflow to `main`. GitHub runs tests first, then publishes and deploys. The image version is `1.1.<workflow run number + 4>`, so the first run is `1.1.5`; rerunning the same Actions run reuses that version. Review the Actions log and Oracle service health after the first deployment.

The Tailscale GitHub Action requires the Tailscale OAuth client to have the `auth_keys` scope and the `tag:ci` tag to be permitted by the tailnet policy. For additional workflow hardening, pin third-party Actions to full commit SHAs and update those pins deliberately.

## Cleanup

From this directory:

```powershell
terraform destroy `
  -var="region=<home-region>" `
  -var="compartment_ocid=<compartment-ocid>" `
  -var="ssh_public_key=<same-public-key>" `
  -var="container_image=<same-image-reference>"
```

Terraform removes the instance, subnet, route table, Internet Gateway, security list, and VCN. It does not remove the registry image or credentials stored on the VM; rotate the Discord/Spotify credentials if the instance was accessible to anyone else.
