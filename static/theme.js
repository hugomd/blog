const toggleMode = theme => {
  const getTheme = window.localStorage && window.localStorage.getItem('theme');
  const isDark = getTheme === 'dark';

  if ((theme === 'light' && isDark) || (theme === 'dark' && !isDark)) {
    document.body.classList.toggle('dark-theme');
    window.localStorage &&
      window.localStorage.setItem(
        'theme',
        document.body.classList.contains('dark-theme') ? 'dark' : 'light',
      );
  }
};

const isDarkMode = window.matchMedia('(prefers-color-scheme: dark)').matches;
const isLightMode = window.matchMedia('(prefers-color-scheme: light)').matches;
const isNotSpecified = window.matchMedia(
  '(prefers-color-scheme: no-preference)',
).matches;

const hasNoSupport = !isDarkMode && !isLightMode && !isNotSpecified;

if (!hasNoSupport) {
	window
		.matchMedia('(prefers-color-scheme: dark)')
		.addListener(e => e.matches && toggleMode('dark'));

	window
		.matchMedia('(prefers-color-scheme: light)')
		.addListener(e => e.matches && toggleMode('light'));
}
