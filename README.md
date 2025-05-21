# SIPBLF Status Service

This is a Go implementation of a SIPBLF Status Service that monitors
Asterisk extensions via AMI and provides real-time updates using
Server-Sent Events (SSE).

## Features

- Real-time extension status monitoring via AMI
- Server-Sent Events for instant updates
- (optional) FreePBX MySQL integration for extension descriptions
- Embedded static files (HTML, CSS, JS)
- Bootstrap-based responsive UI
- Automatic reconnection handling
- Efficient event broadcasting

## Configuration

1. Copy `.env.example` to `.env` and update the values:
   ```bash
   cp .env.example .env
   ```

2. Edit `.env` with your configuration:
   - Server settings:
     * SERVE_IP: IP address to bind to (default: 127.0.0.1)
     * SERVE_PORT: Port to listen on (default: 9000)
   - Authentication:
     * ADMIN_PASSWORD: Admin password
   - AMI credentials:
     * AMI_HOST: Asterisk server address
     * AMI_PORT: AMI port (usually 5038)
     * AMI_USER: AMI username
     * AMI_PASS: AMI password
   - MySQL database connection:
     * DB_HOST: Database server address
     * DB_NAME: Database name
     * DB_USER: Database username
     * DB_PASS: Database password
   - UI Customization:
     * PAGE_TITLE: Page title
     * BRAND_IMAGE: Brand image path
     * BRAND_ALT: Brand image alt text
     * VOIP_IMAGE: VoIP image path
     * VOIP_ALT: VoIP image alt text

If `DB_HOST` is not specified, the service will not attempt to connect to a database
and will not display descriptions for extensions.

## Installation

1. Build the binary:
   ```bash
   go build -o sipblf
   ```

2. Install the systemd service:
   ```bash
   sudo cp sipblf.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now sipblf
   ```

3. Configure Nginx as reverse proxy:
   ```bash
   sudo cp sipblf.conf /etc/nginx/sites-available/
   sudo ln -s /etc/nginx/sites-available/sipblf.conf /etc/nginx/sites-enabled/
   sudo nginx -t
   sudo systemctl reload nginx
   ```

## License

This project is licensed under the MIT License - see the LICENSE file for details.
