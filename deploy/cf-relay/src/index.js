/**
 * Frog Bot V2 — Free Cloudflare Workers + Durable Objects (WebSocket Hibernation) Blind Relay
 *
 * - Runs 100% on the Cloudflare Workers FREE plan (100,000 incoming msgs/day, 0 duration cost while hibernating).
 * - Zero-Knowledge Blind Courier: forwards opaque X25519 + AES-256-GCM encrypted JSON envelopes
 *   between `role=host` (your home Windows/Mac) and `role=client` (your iOS/Android/Mac/Web).
 * - Cloudflare NEVER sees plaintext prompts, API keys, files, or responses.
 */
import { DurableObject } from "cloudflare:workers";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/health") {
      return Response.json({ ok: true, service: "frog-e2ee-cf-relay" });
    }
    if (url.pathname !== "/ws") {
      return new Response("Frog Bot E2EE Blind Relay. Connect via WebSocket /ws", { status: 200 });
    }

    // Optional private relay access token (`wrangler secret put RELAY_TOKEN`)
    if (env.RELAY_TOKEN) {
      const tok =
        url.searchParams.get("token") ||
        (request.headers.get("Authorization") || "").replace(/^Bearer\s+/i, "").trim();
      if (tok !== env.RELAY_TOKEN) {
        return new Response("Unauthorized relay token", { status: 401 });
      }
    }

    const role = (url.searchParams.get("role") || "").toLowerCase();
    const room = (url.searchParams.get("room") || "").toLowerCase();
    const peer = (url.searchParams.get("peer") || "").toLowerCase();
    if (!room || (role !== "host" && role !== "client") || (role === "client" && !peer)) {
      return new Response("Missing role/room/peer", { status: 400 });
    }
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return new Response("Expected WebSocket upgrade", { status: 426 });
    }

    const id = env.RELAY_ROOM.idFromName(room);
    const stub = env.RELAY_ROOM.get(id);
    return stub.fetch(request);
  },
};

export class RelayRoom extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    // Keepalive pings are answered by Cloudflare edge without even waking the Durable Object from hibernation!
    this.ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair("ping", "pong"));
  }

  async fetch(request) {
    const url = new URL(request.url);
    const role = (url.searchParams.get("role") || "").toLowerCase();
    const peer = (url.searchParams.get("peer") || "").toLowerCase();
    const tag = role === "host" ? "host" : `client:${peer}`;

    // Replace any stale socket with the same tag
    for (const old of this.ctx.getWebSockets(tag)) {
      try {
        old.close(1000, "replaced");
      } catch {
        /* ignore */
      }
    }

    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    this.ctx.acceptWebSocket(server, [tag]);
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws, message) {
    if (typeof message !== "string") return;
    if (message === "ping") {
      ws.send("pong");
      return;
    }

    const tags = this.ctx.getTags(ws);
    const myTag = tags[0] || "";
    let parsed;
    try {
      parsed = JSON.parse(message);
    } catch {
      return;
    }

    if (myTag === "host") {
      const targetTag = typeof parsed.to === "string" ? parsed.to : "";
      if (!targetTag.startsWith("client:")) return;
      for (const dst of this.ctx.getWebSockets(targetTag)) {
        try {
          dst.send(message);
        } catch {
          /* ignore */
        }
      }
    } else if (myTag.startsWith("client:")) {
      const hosts = this.ctx.getWebSockets("host");
      if (hosts.length === 0) {
        try {
          ws.send(JSON.stringify({ err: "host_offline" }));
        } catch {
          /* ignore */
        }
        return;
      }
      for (const h of hosts) {
        try {
          h.send(message);
        } catch {
          /* ignore */
        }
      }
    }
  }

  async webSocketClose(ws) {
    try {
      ws.close();
    } catch {
      /* ignore */
    }
  }

  async webSocketError(ws) {
    try {
      ws.close();
    } catch {
      /* ignore */
    }
  }
}
