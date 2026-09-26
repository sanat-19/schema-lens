// Live updates. The server pushes a tiny event whenever the schema changes;
// we then fetch the new schema and patch the graph.
//
// EventSource reconnects on its own if the server restarts, and the first
// thing the server sends on every connection is its current status, so
// after a reconnect we notice if we missed a version.

export function connectLive({ onStatus, onNewVersion }) {
  const events = new EventSource('/api/events');

  const handle = (e) => {
    const status = JSON.parse(e.data);
    onStatus(status);
    onNewVersion(status.version);
  };
  events.addEventListener('status', handle);
  events.addEventListener('schema', handle);

  // The server itself is unreachable (stopped, or the network dropped).
  // EventSource keeps retrying in the background.
  events.addEventListener('error', () => onStatus({ state: 'offline' }));
}
