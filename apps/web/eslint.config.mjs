import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const config = [
  ...nextVitals,
  ...nextTs,
  { ignores: [".next/**", ".open-next/**", "node_modules/**", "next-env.d.ts", "e2e/**"] },
  {
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
      // data loading in effects is the pattern used throughout (usePaged and friends)
      "react-hooks/set-state-in-effect": "off",
      "react-hooks/refs": "off",
    },
  },
];

export default config;
