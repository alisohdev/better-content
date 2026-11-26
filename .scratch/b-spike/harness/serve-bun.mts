import { app } from "./app.mts";

const server = { hostname: "127.0.0.1", port: 3100, fetch: app.fetch };
export default server;
