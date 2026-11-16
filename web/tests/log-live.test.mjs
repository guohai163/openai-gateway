import test from "node:test";
import assert from "node:assert/strict";
import { subscribeRequestLogs } from "../src/log-live.mjs";

function setup(t, onRefresh = async () => {}) {
  const sources = [];
  class FakeEventSource extends EventTarget {
    readyState = 1;
    closed = false;
    constructor(url) {
      super();
      assert.equal(url, "/admin/api/logs/stream");
      sources.push(this);
    }
    emit(event) {
      this.dispatchEvent(new Event(event));
    }
    close() {
      this.closed = true;
    }
  }
  const original = globalThis.EventSource;
  globalThis.EventSource = FakeEventSource;
  t.after(() => {
    if (original === undefined) delete globalThis.EventSource;
    else globalThis.EventSource = original;
  });
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const status = [];
  let unauthorized = 0;
  let errors = 0;
  const stop = subscribeRequestLogs({
    onRefresh,
    onStatus: (value) => status.push(value),
    onUnauthorized: () => unauthorized++,
    onError: () => errors++,
  });
  t.after(stop);
  return { sources, status, stop, unauthorized: () => unauthorized, errors: () => errors };
}

test("bursts coalesce and events during a fetch schedule a subsequent refresh", async (t) => {
  let calls = 0;
  let release;
  const fixture = setup(t, () => {
    calls++;
    return new Promise((resolve) => { release = resolve; });
  });
  const source = fixture.sources[0];
  source.emit("ready");
  for (let i = 0; i < 100; i++) source.emit("logs");
  t.mock.timers.tick(299);
  assert.equal(calls, 0);
  t.mock.timers.tick(1);
  assert.equal(calls, 1);
  source.emit("logs");
  t.mock.timers.tick(1000);
  assert.equal(calls, 1, "refresh requests must not overlap");
  release();
  await Promise.resolve();
  t.mock.timers.tick(300);
  assert.equal(calls, 2, "updates arriving during a fetch must not be lost");
  release();
  await Promise.resolve();
  assert.equal(fixture.status.at(-1), "live");
});

test("database and browser reconnects refresh the current list", async (t) => {
  let calls = 0;
  const fixture = setup(t, async () => { calls++; });
  const source = fixture.sources[0];
  source.emit("reconnecting");
  assert.equal(fixture.status.at(-1), "reconnecting");
  source.emit("ready");
  t.mock.timers.tick(300);
  await Promise.resolve();
  assert.equal(calls, 1);
  source.readyState = 2;
  source.onerror();
  assert.equal(fixture.errors(), 1);
  t.mock.timers.tick(3000);
  assert.equal(fixture.sources.length, 2);
  fixture.sources[1].emit("ready");
  t.mock.timers.tick(300);
  await Promise.resolve();
  assert.equal(calls, 2);
});

test("pause or navigation closes the connection and cancels pending refreshes", (t) => {
  let calls = 0;
  const fixture = setup(t, async () => { calls++; });
  const source = fixture.sources[0];
  source.emit("logs");
  fixture.stop();
  t.mock.timers.tick(5000);
  source.emit("logs");
  assert.equal(calls, 0);
  assert.equal(source.closed, true);
});

test("expired sessions stop the stream without reconnecting", (t) => {
  const fixture = setup(t);
  fixture.sources[0].emit("unauthorized");
  assert.equal(fixture.unauthorized(), 1);
  assert.equal(fixture.sources[0].closed, true);
  t.mock.timers.tick(5000);
  assert.equal(fixture.sources.length, 1);
});
