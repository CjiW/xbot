---
title: "QQ & NapCat"
weight: 40
---

# QQ & NapCat Channels

Two QQ integration options: native QQ Bot API and NapCat (OneBot 11 protocol).

## QQ Bot (Official API)

Native WebSocket-based QQ Bot channel using the official API.

### Setup

1. Create a bot on the [QQ Open Platform](https://q.qq.com/).
2. Enable WebSocket mode.
3. Configure:

```bash
QQ_ENABLED=true
QQ_APP_ID=xxxxxxxxxx
QQ_CLIENT_SECRET=xxxxxxxxxx
```

### Access Control

```bash
QQ_ALLOW_FROM=openid1,openid2,openid3
```

## NapCat (OneBot 11)

Compatible with [NapCat](https://github.com/NapNeko/NapCatQQ) and other OneBot 11 implementations. Connects via WebSocket with exponential backoff reconnection.

### Setup

1. Deploy [NapCat](https://github.com/NapNeko/NapCatQQ) and configure WebSocket.
2. Configure xbot:

```bash
NAPCAT_ENABLED=true
NAPCAT_WS_URL=ws://127.0.0.1:3001
NAPCAT_TOKEN=your_token_here
```

### Access Control

```bash
NAPCAT_ALLOW_FROM=123456789,987654321
```

## Differences

| Feature | QQ Bot | NapCat |
|---------|--------|--------|
| Protocol | QQ Official API | OneBot 11 (WebSocket) |
| Message types | Text, markdown, images, files | Text, images, files |
| Group support | Yes | Yes |
| Private chat | Yes | Yes |
| Card messages | No | No |
| Setup complexity | Requires QQ Open Platform registration | Self-hosted NapCat instance |

## Troubleshooting

| Issue | Solution |
|-------|----------|
| WebSocket connection fails | Check NapCat is running and WS URL is correct |
| Messages not received | Verify allow-list includes the sender's ID |
| Reconnection loops | Check network stability; NapCat has exponential backoff (max 5 min) |
