import colors from 'tailwindcss/colors';

/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{vue,ts,js}"],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        surface: {
          DEFAULT: "#0a0f1a",
        },
        // 提供完整色阶，保证可用 text-brand-200 等类
        brand: colors.indigo,
        state: {
          success: "#10b981", // emerald-500
          warning: "#f59e0b", // amber-500
          danger:  "#f43f5e", // rose-500
        },
      },
      borderRadius: {
        xl: "0.75rem",
      },
      boxShadow: {
        card: "0 10px 20px rgba(0,0,0,0.25), 0 2px 6px rgba(0,0,0,0.2)",
      },
      opacity: {
        15: "0.15",
      },
    },
  },
  plugins: [
    function({ addBase, addComponents, addUtilities, theme }) {
      addBase({
        'html, body, #app': {
          height: '100%',
          overflow: 'hidden',
          backgroundColor: '#0b1220',
        }
      });
      addComponents({
        '.card-base': {
          '@apply bg-slate-900/50 border border-slate-800 rounded-xl shadow-card': {},
        },
        '.card-padding': {
          '@apply p-4 md:p-6': {},
        },
        '.header-bar': {
          '@apply h-14 flex items-center justify-between px-4 md:px-6 border-b border-slate-800 bg-slate-950/60': {},
        },
        '.tab-bar': {
          '@apply flex items-center gap-2 px-4 md:px-6 py-2 border-b border-slate-800 bg-slate-950/40': {},
        },
        '.tab-btn': {
          '@apply text-xs px-3 py-1.5 rounded border border-slate-800 bg-slate-900/40 text-slate-300': {},
        },
        '.tab-btn-active': {
          '@apply bg-brand-500/20 border-brand-500/60 text-brand-200': {},
        },
        '.status-dot': {
          '@apply w-2 h-2 rounded-full inline-block align-middle': {},
        },
        '.status-success': { backgroundColor: theme('colors.state.success') },
        '.status-warning': { backgroundColor: theme('colors.state.warning') },
        '.status-danger':  { backgroundColor: theme('colors.state.danger') },
        '.sidebar': {
          '@apply w-72 shrink-0 h-full border-r border-slate-800 bg-slate-950/80': {},
        },
        '.sidebar-item': {
          '@apply px-4 py-2 text-sm text-slate-300 hover:bg-slate-800/40 cursor-pointer flex items-center justify-between': {},
        },
        '.sidebar-item-active': {
          '@apply bg-indigo-500/10 border-l-4 border-indigo-500 text-indigo-200': {},
        },
        '.scroll-area': {
          '@apply overflow-auto': {},
        },
        '.progress-track': {
          '@apply h-2 bg-slate-800 rounded-full overflow-hidden': {},
        },
        '.progress-bar-emerald': {
          '@apply h-full bg-emerald-500 transition-[width] duration-300 ease-out': {},
        },
        '.progress-bar-sky': {
          '@apply h-full bg-sky-400 transition-[width] duration-300 ease-out': {},
        },
        '.toast-banner': {
          '@apply fixed top-0 inset-x-0 z-50 px-4 py-2 text-xs text-slate-200 flex items-center justify-center': {},
          background: "linear-gradient(to bottom, rgba(99,102,241,.25), rgba(2,6,23,0.15))",
          backdropFilter: "saturate(140%) blur(6px)",
          borderBottom: "1px solid rgba(30,41,59,.7)"
        },
      });
      addUtilities({
        '.scroll-dark::-webkit-scrollbar': { width: '10px', height: '10px' },
        '.scroll-dark::-webkit-scrollbar-track': { background: 'rgba(2,6,23,0.6)' },
        '.scroll-dark::-webkit-scrollbar-thumb': {
          background: 'rgba(51,65,85,0.7)',
          borderRadius: '8px',
          border: '2px solid rgba(2,6,23,0.6)',
        },
        '.scroll-dark::-webkit-scrollbar-thumb:hover': { background: 'rgba(100,116,139,0.7)' },
        '.banner-success': { background: 'linear-gradient(to bottom, rgba(16,185,129,.25), rgba(2,6,23,0.15))' },
        '.banner-warning': { background: 'linear-gradient(to bottom, rgba(245,158,11,.25), rgba(2,6,23,0.15))' },
        '.banner-danger':  { background: 'linear-gradient(to bottom, rgba(244,63,94,.25), rgba(2,6,23,0.15))' },
      });
    }
  ],
};

