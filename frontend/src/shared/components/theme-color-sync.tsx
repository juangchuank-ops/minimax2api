import { useTheme } from "next-themes";
import { useEffect } from "react";

/**
 * Keep the `theme-color` metas in step with the console's own theme.
 *
 * index.html ships two metas scoped by `prefers-color-scheme`. That is right for
 * the default "system" setting, but wrong the moment someone picks Light or Dark
 * explicitly: the browser UI would keep following the OS while the console does
 * not. So once the resolved theme settles, both metas are rewritten to the same
 * colour — the media query still decides which one applies, and whichever one
 * applies is then correct either way.
 *
 * The colours mirror `--background` in index.css. They are duplicated rather
 * than read from CSS because a meta tag has to be right at first paint, and
 * reading a custom property would need the stylesheet parsed and an element
 * mounted first.
 */
const BACKGROUND = { light: "#fdfdfd", dark: "#070707" } as const;

export function ThemeColorSync() {
  const { resolvedTheme } = useTheme();

  useEffect(() => {
    // Before hydration resolvedTheme is undefined. Leave the metas to the media
    // queries rather than guessing a value and flashing the wrong one.
    if (!resolvedTheme) {
      return;
    }
    const colour = resolvedTheme === "dark" ? BACKGROUND.dark : BACKGROUND.light;
    const metas = document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]');
    for (const meta of metas) {
      meta.content = colour;
    }
  }, [resolvedTheme]);

  return null;
}
