// Live updates. The server pushes a tiny event whenever the schema changes
// or it switches to another source; the page then fetches what it needs.
//
// The first thing the server sends on every connection is its current
// status, so after a reconnect we notice anything we missed.

// retryAfter is how long to wait before opening a new stream when the old
// one was closed for good.
const retryAfter = 2000;

export function connectLive({ onStatus, onOffline }) {
  const events = new EventSource('/api/events');

  const handle = (e) => onStatus(JSON.parse(e.data));
  events.addEventListener('status', handle);
  events.addEventListener('schema', handle);

  // The SchemaLens server itself is unreachable (stopped, or the network
  // dropped). This says nothing about what's on screen, so it's reported
  // separately.
  //
  // EventSource retries a dropped connection on its own, but gives up for
  // good on an HTTP error, which is what the Vite dev server answers while
  // the backend is down. Then we start over with a new one.
  events.addEventListener('error', () => {
    onOffline();
    if (events.readyState === EventSource.CLOSED) {
      setTimeout(() => connectLive({ onStatus, onOffline }), retryAfter);
    }
  });
}
