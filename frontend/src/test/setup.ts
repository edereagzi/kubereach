import { cleanup } from "@testing-library/react";
import { Call as WailsCall, objectNames, setTransport } from "@wailsio/runtime";
import { afterEach } from "vite-plus/test";

type Call = { methodID: number; args: unknown[] };

// backend is the fake Go side. It keeps each call of a bound method and gives the answer that the test sets.
export const backend = {
  calls: [] as Call[],
  answer: (_call: Call): unknown => undefined,
};

let captured: Call[] | null = null;

setTransport({
  call: async (object, _method, _window, options) => {
    if (object !== objectNames.Call) return undefined;
    const { methodID, args } = options as { methodID: number; args: unknown[] };
    const call = { methodID, args };
    if (captured) {
      captured.push(call);
      return undefined;
    }
    backend.calls.push(call);
    return backend.answer(call);
  },
});

afterEach(() => {
  cleanup();
  backend.calls = [];
  backend.answer = () => undefined;
});

// callOf gives the call that a binding sends, so that a test can compare it with backend.calls.
export function callOf(send: () => unknown) {
  captured = [];
  void send();
  const [call] = captured;
  captured = null;
  return call!;
}

// failure is an error as the Go side sends it: main.go puts the message for the user and its code on the cause.
export const failure = (message: string, code?: string) =>
  Object.assign(new WailsCall.RuntimeError("raw Go error"), { cause: { message, code } });
