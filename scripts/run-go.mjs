import { spawn } from "node:child_process";

const [command, ...args] = process.argv.slice(2);
if (!command) {
  process.stderr.write("Usage: node scripts/run-go.mjs <go-command> [...args]\n");
  process.exit(2);
}

const child = spawn("go", ["-C", "backend", command, ...args], {
  env: process.env,
  stdio: "inherit",
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("error", (error) => {
  process.stderr.write(`Could not start Go: ${error.message}\n`);
  process.exitCode = 1;
});

child.on("exit", (code, signal) => {
  process.exitCode = signal ? 1 : (code ?? 1);
});
