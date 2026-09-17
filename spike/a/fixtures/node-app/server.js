// Spike A Node fixture: zero-dependency HTTP server.
// Railpack should detect this as a Node app (package.json + start script)
// and build it without any npm registry round-trip (no dependencies).
const http = require('http');

const PORT = process.env.PORT || 3000;
const BUILD_STAMP = process.env.BUILD_STAMP || 'unset';

const server = http.createServer((req, res) => {
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end(`spike-a-node-app OK stamp=${BUILD_STAMP}\n`);
});

server.listen(PORT, () => {
  console.log(`listening on ${PORT}, stamp=${BUILD_STAMP}`);
});
