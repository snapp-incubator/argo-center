import { Badge, Button, createTheme, Paper, type MantineColorsTuple } from '@mantine/core';

// The palette, type and radii follow the argo-center design mockup. Surface, border and
// status colours that differ between light and dark live as CSS variables in global.css.
const brand: MantineColorsTuple = [
  '#EEF2FC',
  '#DCE4F7',
  '#B7C8F0',
  '#8FA9E8',
  '#6A8DE1',
  '#4673DB',
  '#2457D6',
  '#1F4BB8',
  '#193D96',
  '#132F75',
];

export const theme = createTheme({
  primaryColor: 'brand',
  primaryShade: { light: 6, dark: 5 },
  colors: { brand },
  fontFamily: "'Inter Variable', Inter, system-ui, -apple-system, 'Segoe UI', sans-serif",
  fontFamilyMonospace: "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, SFMono-Regular, monospace",
  headings: {
    fontFamily: "'Inter Variable', Inter, system-ui, -apple-system, 'Segoe UI', sans-serif",
    fontWeight: '700',
    sizes: {
      h1: { fontSize: '26px', lineHeight: '1.25' },
      h2: { fontSize: '24px', lineHeight: '1.25' },
      h3: { fontSize: '18px', lineHeight: '1.3' },
      h4: { fontSize: '16px', lineHeight: '1.35' },
    },
  },
  fontSizes: { xs: '12px', sm: '13px', md: '14px', lg: '16px', xl: '20px' },
  radius: { xs: '4px', sm: '6px', md: '8px', lg: '10px', xl: '12px' },
  defaultRadius: 'md',
  components: {
    Badge: Badge.extend({ defaultProps: { tt: 'none', radius: 'xl' } }),
    Button: Button.extend({ defaultProps: { radius: 'md' } }),
    Paper: Paper.extend({ defaultProps: { radius: 'lg' } }),
  },
});
