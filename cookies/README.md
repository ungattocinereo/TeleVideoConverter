# Cookie Authentication Setup

This directory contains browser cookies in Netscape format for downloading videos from platforms that require authentication (like Instagram private accounts).

## Why Cookies Are Needed

Some platforms (especially Instagram) require you to be logged in to view certain content:
- Private Instagram profiles
- Instagram stories
- Age-restricted content
- Region-locked videos

## How to Export Cookies

### Method 1: Using Browser Extension (Recommended)

#### For Chrome/Edge:
1. Install the "Get cookies.txt LOCALLY" extension from Chrome Web Store
   - Link: https://chrome.google.com/webstore (search for "Get cookies.txt LOCALLY")

2. Log in to Instagram (or other platform) in your browser

3. Visit the Instagram website while logged in (https://www.instagram.com)

4. Click the extension icon in your browser toolbar

5. Click "Export" to download the cookies.txt file

6. Save the downloaded file in this directory with the correct name:
   - Instagram: `instagram.txt`
   - Facebook: `facebook.txt`
   - Twitter/X: `twitter.txt`

#### For Firefox:
1. Install the "cookies.txt" extension by Lennon Hill
   - Link: https://addons.mozilla.org/firefox/ (search for "cookies.txt")

2. Follow steps 2-6 from Chrome instructions above

### Method 2: Manual Export (Advanced)

If you prefer manual export, cookies must be in Netscape format:
```
# Netscape HTTP Cookie File
.instagram.com    TRUE    /    TRUE    1234567890    cookie_name    cookie_value
```

## Setup Instructions

1. **Export cookies** for the platforms you need (see above)

2. **Save the cookie file** in this directory with the correct filename:
   - Instagram → `instagram.txt`
   - Facebook → `facebook.txt`
   - Twitter/X → `twitter.txt`
   - TikTok → `tiktok.txt`

3. **Restart the downloader service**:
   ```bash
   docker compose restart downloader
   ```

4. **Test with a video URL** via Telegram bot or web interface

## Security Notes

⚠️ **Important Security Information:**
- Cookie files contain authentication tokens - **never share them with anyone**
- Cookies are accessible by the downloader container (yt-dlp needs to update session data)
- Cookie files are **excluded from git** via .gitignore (they won't be committed)
- Cookies are stored **locally only** and never transmitted over the network
- Only authorized users (IDs: 100269722, 40882420) can trigger downloads

## Supported Platforms

Currently configured platforms:
- **Instagram** (`instagram.txt`) - Private profiles, stories, age-restricted
- **Facebook** (`facebook.txt`) - Private videos, restricted content
- **Twitter/X** (`twitter.txt`) - Protected accounts, private videos
- **TikTok** (`tiktok.txt`) - Private/restricted videos, age-gated content

You can add more platforms by creating cookie files with the appropriate names.

## Troubleshooting

### "Authentication required" error

**Symptoms:** Bot returns error message about authentication or login required

**Solutions:**
1. Check if the cookie file exists in this directory
   ```bash
   ls -la cookies/
   ```

2. Verify the filename matches exactly (e.g., `instagram.txt`, not `Instagram.txt`)

3. Ensure you were logged in when exporting cookies

4. Try re-exporting fresh cookies (old ones may have expired)

5. Restart the downloader service after adding cookies:
   ```bash
   docker compose restart downloader
   ```

6. Check downloader logs for cookie-related messages:
   ```bash
   docker compose logs downloader | grep -i cookie
   ```

### Videos still not downloading

**Symptoms:** Download fails even with cookies present

**Solutions:**
1. **Verify cookie file format** - must be Netscape format (starts with `# Netscape HTTP Cookie File`)

2. **Check file permissions** - ensure the file is readable:
   ```bash
   chmod 644 cookies/instagram.txt
   ```

3. **Test if you can view the content while logged in** - if you can't see it in your browser while logged in, the bot can't download it either

4. **Check cookie expiration** - cookies typically expire after 30-90 days (see below)

5. **Review downloader logs** for detailed error messages:
   ```bash
   docker compose logs --tail=50 downloader
   ```

### Verifying cookie installation

Check if cookies are properly mounted in the container:
```bash
# List cookies directory in container
docker compose exec downloader ls -la /cookies

# View first 5 lines of cookie file (safe to display)
docker compose exec downloader head -5 /cookies/instagram.txt
```

## Cookie Expiration

Cookies have limited lifespans and need to be refreshed periodically:

| Platform | Typical Expiration | Signs of Expiration |
|----------|-------------------|---------------------|
| Instagram | 30-90 days | Authentication errors, login required |
| Facebook | 30-60 days | Can't access private content |
| Twitter | 30 days | Protected account access fails |
| TikTok | 30-60 days | Can't access private/restricted videos |

**What to do when cookies expire:**
1. Export fresh cookies from your browser (follow setup instructions above)
2. Replace the old cookie file with the new one
3. Restart the downloader: `docker compose restart downloader`
4. Test with a video URL

**You'll know cookies have expired when:**
- The bot shows authentication errors
- Previously working private content URLs now fail
- Error messages mention "login" or "authentication required"

## Adding More Platforms

To add support for additional platforms:

1. **Export cookies** for the new platform using browser extension

2. **Save cookie file** with appropriate name (e.g., `tiktok.txt`, `reddit.txt`)

3. **Update configuration** in `downloader/ytdlp_wrapper.py`:
   ```python
   platform_map = {
       'instagram.com': 'instagram.txt',
       'facebook.com': 'facebook.txt',
       'twitter.com': 'twitter.txt',
       'x.com': 'twitter.txt',
       'tiktok.com': 'tiktok.txt',  # Add new platforms here
   }
   ```

4. **Rebuild and restart**:
   ```bash
   docker compose build downloader
   docker compose restart downloader
   ```

## Privacy and Data Protection

🔒 **How your cookies are protected:**

1. **Local storage only** - Cookies never leave your server
2. **No network transmission** - Not sent to any external service
3. **Git exclusion** - Automatically excluded from version control
4. **Container isolation** - Only accessible by downloader service
5. **Access control** - Only 2 authorized Telegram users can use the bot
6. **No logging** - Cookie contents are never logged (only file paths)

**Best practices:**
- Export cookies from a browser session you trust
- Use a dedicated browser profile for bot-related cookies (optional)
- Refresh cookies regularly (every 30-60 days)
- Don't copy cookie files to other machines
- Keep this server secure with SSH keys and firewall rules

## FAQ

### Q: Do I need cookies for YouTube?
**A:** No, YouTube videos work without cookies (unless they're private/unlisted)

### Q: Can I use the same cookie file for multiple users?
**A:** Yes, one Instagram cookie file works for both authorized bot users

### Q: Will cookies slow down downloads?
**A:** No, there's no performance impact

### Q: Can I use cookies from mobile browsers?
**A:** It's complicated - desktop browser cookies are easier to export

### Q: What if I don't want to use cookies?
**A:** You can still use the bot for public content without cookies

### Q: Are cookies required for all Instagram videos?
**A:** No, only for private profiles, stories, and age-restricted content

## Need Help?

If you encounter issues not covered here:

1. Check the downloader logs:
   ```bash
   docker compose logs --tail=100 downloader
   ```

2. Verify your setup:
   ```bash
   # Check if directory is mounted
   docker compose exec downloader ls -la /cookies

   # Check environment variable
   docker compose exec downloader printenv COOKIES_PATH
   ```

3. Test with a known working URL first (public Instagram video)

4. Try exporting cookies again from a fresh browser session
