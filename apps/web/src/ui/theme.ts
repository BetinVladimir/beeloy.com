import { Anchor, Card, Chip, createTheme, type CSSVariablesResolver, type MantineColorsTuple } from "@mantine/core";

// Brand yellow. Shade 6 stays the bright fill (text on it is auto-contrasted to dark);
// shades 8–9 are the dark ones used for text and links so they meet WCAG AA on white.
const brand: MantineColorsTuple = [
  "#fff9db", "#fff3bf", "#ffec99", "#ffe066", "#ffd43b", "#fcc419", "#fab005", "#946200", "#7a5000", "#5f3e00",
];

// Near-black news-site palette: body, raised surface, hairline borders.
const dark: MantineColorsTuple = [
  "#e8eaed", "#c4c9d0", "#a6adb4", "#7d8590", "#2a2f37", "#232830", "#14171c", "#0b0d10", "#07080a", "#000000",
];

export const theme = createTheme({
  colors: { brand, dark },
  primaryColor: "brand",
  primaryShade: { light: 6, dark: 6 },
  autoContrast: true,
  luminanceThreshold: 0.4,
  components: {
    Anchor: Anchor.extend({ defaultProps: { underline: "always" } }),
    Card: Card.extend({ defaultProps: { radius: "sm", padding: "md" } }),
    Chip: Chip.extend({ defaultProps: { variant: "light" } }), // the filled variant would put white text on the bright brand yellow
  },
  defaultRadius: "sm",
  fontFamily: "Inter, system-ui, -apple-system, Segoe UI, Roboto, sans-serif",
  headings: { fontFamily: "Inter, system-ui, -apple-system, Segoe UI, Roboto, sans-serif" },
});

export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: {
    "--mantine-color-anchor": "#7a5000",
    "--mantine-color-dimmed": "#5c636a",
    "--mantine-color-brand-light-color": "#7a5000",
  },
  dark: {
    "--mantine-color-anchor": "#ffd43b",
    "--mantine-color-dimmed": "#a6adb4",
    "--mantine-color-brand-light-color": "#ffd43b",
  },
});
