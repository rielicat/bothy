// Bothy's picture in the chat apps (docs/design/ui.md §11.3, §12.3; SPEC §8.3
// /api/agent/picture). Telegram and Discord take it from Bothy; Slack lets
// only a person change an app's icon, so the UI hands the file over instead.

/** The picture itself; a new version makes the browser fetch the new one. */
export const pictureURL = (version = '') => (version ? `/api/agent/picture?v=${encodeURIComponent(version)}` : '/api/agent/picture');

/** The same picture as a file named after Bothy. */
export const pictureDownloadURL = '/api/agent/picture?download=1';

/** The app's Basic Information page, where App icon is, or the list of apps until Slack has said which one is Bothy. */
export const slackAppURL = (appId?: string) => (appId ? `https://api.slack.com/apps/${encodeURIComponent(appId)}/general` : 'https://api.slack.com/apps');

/** The server's own line for Slack (PictureAppResult.detail, `manual`), said before any change too. */
export const slackPictureLine = "In Slack, upload it under App icon in Slack's app settings.";

/** Larger than this, the server refuses the upload (internal/picture.MaxUpload). */
export const pictureMaxBytes = 5 << 20;
export const pictureTooBig = 'That picture is over 5 MB. Choose a smaller one.';
