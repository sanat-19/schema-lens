// Live updates. The server pushes a tiny event whenever the schema changes
// or it switches to another source; the page then fetches what it needs.
//
// EventSource reconnects on its own if the server restarts, and the first
// thing the server sends on every connection is its current status, so
// after a reconnect we notice anything we missed.

export function connectLive({ onStatus, onOffline }) {
  const events = new EventSource('/api/events');

  const handle = (e) => onStatus(JSON.parse(e.data));
  events.addEventListener('status', handle);
  events.addEventListener('schema', handle);

  // The SchemaLens server itself is unreachable (stopped, or the network
  // dropped). This says nothing about what's on screen, so it's reported
  // separately; EventSource keeps retrying in the background.
  events.addEventListener('error', () => onOffline());
}
