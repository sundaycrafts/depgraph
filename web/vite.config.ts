/// <reference types="vitest/config" />
import path from "path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
    plugins: [react(), tailwindcss()],
    resolve: {
        alias: { "@": path.resolve(__dirname, "src") },
    },
    server: {
        proxy: {
            "/graph": {
                target: "http://localhost:8080",
                configure: (proxy) => {
                    // Suppress ECONNREFUSED noise while the Go backend is still starting up.
                    proxy.on("error", (_err, _req, res) => {
                        const r = res as import("http").ServerResponse;
                        if (!r.headersSent) r.writeHead(503).end();
                    });
                },
            },
            "/file": {
                target: "http://localhost:8080",
                configure: (proxy) => {
                    proxy.on("error", (_err, _req, res) => {
                        const r = res as import("http").ServerResponse;
                        if (!r.headersSent) r.writeHead(503).end();
                    });
                },
            },
        },
    },
    test: {
        environment: "jsdom",
        setupFiles: ["./src/test/setup.ts"],
        globals: true,
    },
});
