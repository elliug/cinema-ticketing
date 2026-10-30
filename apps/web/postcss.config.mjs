// Tailwind's PostCSS entry point (M3.5 · T3b).
//
// Tailwind v4 is configured in CSS (see src/app/globals.css), so this file only has
// to register the plugin: there is no tailwind.config.js to keep in sync.
const config = {
  plugins: {
    "@tailwindcss/postcss": {},
  },
};

export default config;
