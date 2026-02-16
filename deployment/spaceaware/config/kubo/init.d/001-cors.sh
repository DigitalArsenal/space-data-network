#!/bin/sh
# Configure CORS so the IPFS WebUI served via SDN admin can talk to Kubo RPC.
# SDN reverse-proxies /api/v0/* and strips Origin/Referer, but the WebUI
# also makes direct requests when configured with a custom API URL.

ipfs config --json API.HTTPHeaders.Access-Control-Allow-Origin \
  '["https://spaceaware.io", "http://localhost:3000", "http://127.0.0.1:5001", "https://webui.ipfs.io"]'

ipfs config --json API.HTTPHeaders.Access-Control-Allow-Methods \
  '["PUT", "POST"]'
