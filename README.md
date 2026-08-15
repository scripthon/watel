# WATEL

**The Telegram–WhatsApp Relay**

Bridges WhatsApp into a single Telegram group, giving every WhatsApp contact
its own **topic**: when number A messages you, the topic for A is created if it
does not exist yet and the message lands there. When B messages you, a topic
for B is created too, and so on.

Anything you type inside a topic is sent back to that contact on WhatsApp, so
Telegram becomes your WhatsApp inbox.

![On the left, a WhatsApp chat list with private conversations from Budi, Siti,
Andi, Dimas, Rina and Maya. On the right, the same six conversations as
separate topics inside one Telegram supergroup, with a topic opened to show a
reply being typed back to Budi.](watel.jpg)

Current scope: **private chats only**. WhatsApp groups, status updates and
broadcasts are deliberately ignored.

## How it works

```
WhatsApp (whatsmeow, multi-device)
        │  message arrives from number X
        ▼
   bridge  ──►  look up X → topic_id in SQLite
        │           ├─ missing → createForumTopic, store the mapping
        │           └─ found   → reuse that topic
        ▼
Telegram supergroup (Topics enabled)  ──►  #Budi (+62812…)   #Siti (+62813…)
        │  you reply inside a topic
        ▼
   bridge  ──►  topic_id → JID  ──►  send to WhatsApp
```

The mapping lives in `data/bridge.db`, the WhatsApp session in
`data/whatsapp.db`. They are kept separate so that re-pairing WhatsApp never
wipes your topic mapping.

## Setup

### 1. Create the Telegram bot

1. Message [@BotFather](https://t.me/BotFather) → `/newbot` → keep the token.
2. `/setprivacy` → pick your bot → **Disable**. Do this **before** adding the
   bot to the group (see the privacy mode note below).

### 2. Prepare the group

1. Create a group, then make it a **supergroup** (adding a bot or flipping it
   to public and back is enough; Telegram upgrades it automatically).
2. Manage group → **Topics** → enable.
3. Add the bot **directly as an admin**, with the **Manage Topics** permission
   turned on.
4. Get the group id: post any message in the group, then open
   `https://api.telegram.org/bot<TOKEN>/getUpdates` and read `chat.id`
   (a negative number, usually starting with `-100`).

#### About privacy mode

By default a Telegram bot in a group runs in *privacy mode*: it only receives
commands aimed at it, mentions, and replies to its own messages — not ordinary
messages. If that applies here, WhatsApp → Telegram still works, but **your
replies never reach WhatsApp**, with no error message at all.

Telegram's documentation states that a bot *added to a group as an admin*
always receives every message, so step 3 above is already sufficient. Turning
privacy mode off via `/setprivacy` is a second layer of safety: it costs
nothing and covers the case where the admin rights are accidentally removed.

One trap: changing `/setprivacy` **only takes effect after the bot is re-added**
to the group. If you already added the bot and disabled it afterwards, remove
the bot from the group and add it again. This is the most common cause of
"I disabled it and it still does not work".

### 3. Configure

```bash
cp .env.example .env
# fill in TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID
```

Set `TELEGRAM_OWNER_ID` to your own Telegram user id if the group has other
members and you do not want them sending messages to WhatsApp.

### 4. Run

```bash
go build -o watel ./cmd/watel
./watel
```

On the first run a QR code appears in the terminal. Scan it from
**WhatsApp → Linked devices → Link a device**.

If your terminal cannot render the QR code (over SSH, for example), set
`WA_PAIR_PHONE` to your number in international format without `+`. The bridge
then prints an 8-digit code to enter under
**Linked devices → Link with phone number**.

The session is persisted, so later runs connect straight away without pairing.

## Running with Docker Compose

Credentials are read from the same `.env` as a local run, so finish step 3
first.

The very first run has to be interactive, because pairing prints a QR code
that needs a real terminal:

```bash
docker compose run --rm watel
```

Pair the device, then stop it with Ctrl-C. The session now lives on the
`watel-data` volume, so from here on:

```bash
docker compose up -d          # start in the background
docker compose logs -f        # follow the log
docker compose down           # stop (the volume, and the pairing, survive)
```

After changing the code, rebuild with `docker compose up -d --build`.

Notes:

- The image builds a static binary with `CGO_ENABLED=0` (the sqlite driver is
  pure Go), so the runtime layer only needs `ca-certificates`.
- Both databases live on the `watel-data` volume. Keep it, or you will have to
  pair again. `docker compose down -v` deletes it.
- The bridge runs as uid 10001. The named volume inherits the right ownership
  automatically; if you switch to a bind mount, `chown 10001` it first or the
  databases cannot be written.
- On a headless server, set `WA_PAIR_PHONE` in `.env` and pair with an 8-digit
  code instead of a QR — much easier than reading a QR out of the logs.
- Timestamps use `Asia/Jakarta` by default. Override it by setting `TZ` in
  `.env` (e.g. `TZ=Asia/Makassar`); `tzdata` is installed in the image so any
  IANA zone works. A local, non-Docker run follows the machine's own timezone
  instead.
- `docker compose config` prints every resolved value, including your bot
  token in cleartext. Handy for debugging, careful where you paste it.

## Telegram commands

They work in the General tab as well as inside a topic.

| Command | Purpose |
| --- | --- |
| `/new <number>` | Open a topic for a number that has never written, e.g. `/new 6281234567890` |
| `/whois` | Show which WhatsApp chat the current topic is bound to |
| `/status` | Connection state and number of bridged chats |
| `/help` | This help text |

## What is supported

| Direction | Text | Photo | Video | Voice/audio | Document | Sticker | Location/contact/poll |
| --- | --- | --- | --- | --- | --- | --- | --- |
| WhatsApp → Telegram | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ (text summary) |
| Telegram → WhatsApp | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — |

Behaviour worth knowing:

- **Captions and replies** are carried over; a message replying to another one
  gets an `↩️ re: …` line.
- **Messages you send from your own phone** are mirrored into the topic with an
  `↪️ you:` prefix (turn it off with `MIRROR_OWN_MESSAGES=false`).
- **Replying from Telegram** also marks that chat as read on your phone, and
  your Telegram message gets a 👍 reaction once it has actually been delivered.
- **Size limits**: Telegram caps bots at 50 MB for uploads and 20 MB for
  downloads. Anything above that is not transferred; a note appears in the
  topic instead, so nothing disappears silently.
- **View-once messages** cannot be relayed. WhatsApp deliberately withholds
  their content from linked devices — the server only sends an empty
  placeholder (`<unavailable type="view_once">`), and the follow-up request to
  the phone is refused. The bridge posts an "open it on your phone" note in the
  matching topic so you still know something arrived. This is a platform
  restriction, not something code can work around.
- **A deleted topic** in Telegram is detected from the send error: the bridge
  creates a fresh topic for that contact and re-delivers the message.
- **Hidden numbers (LID)**: WhatsApp now sometimes uses `@lid` identities
  instead of phone numbers. The bridge normalises them back to the phone number
  when it is known, so one contact does not end up with two topics.

## Notes

- This is built on [whatsmeow](https://github.com/tulir/whatsmeow), a
  multi-device WhatsApp Web library. As with any WhatsApp Web client, your
  account can be restricted if it is used for spam. Use it for your own account.
- The bridge serves exactly one WhatsApp account and one Telegram group.
- Media is never written to disk; the bytes pass through memory and are
  forwarded straight on.
