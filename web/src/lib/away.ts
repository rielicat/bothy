// Full-page navigation out of the app, to a provider's sign-in page. An
// object so tests can swap `to` for a spy (jsdom can't navigate).
export const away = {
  to(url: string) {
    window.location.assign(url);
  },
};
