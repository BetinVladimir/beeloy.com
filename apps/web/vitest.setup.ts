import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => cleanup());

// Mantine needs these browser APIs which jsdom lacks
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({ matches: false, media: query, onchange: null, addListener: () => {}, removeListener: () => {}, addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false }),
});
class RO { observe() {} unobserve() {} disconnect() {} }
(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO;

// Mantine's autosizing Textarea listens to font loading
Object.defineProperty(document, "fonts", { value: { addEventListener() {}, removeEventListener() {}, ready: Promise.resolve() }, configurable: true });
