import { useEffect, useRef } from 'react';

// Monochroma: YouTube Music tracks are fetched/remuxed server-side on first
// play, which costs seconds. Navidrome exposes /rest/prewarmYTM.view to warm
// that path (resolve the CDN URL, or pre-download for Apple clients) before
// the track is reached. This hook derives the prewarm URL from an
// already-resolved, authenticated stream URL — so it needs no access to
// credentials — and fires it once per track. Called for both players, it warms
// the current track and the next queued one while the current plays.

function prewarmUrlFromStreamUrl(streamUrl: string): null | string {
    try {
        const url = new URL(streamUrl);
        if (!/\/rest\/(stream|getTranscodeStream)\.view$/.test(url.pathname)) {
            return null;
        }
        const id = url.searchParams.get('id') ?? url.searchParams.get('mediaId');
        if (!id || !id.startsWith('ytm-track-')) {
            return null;
        }
        url.pathname = url.pathname.replace(
            /\/(stream|getTranscodeStream)\.view$/,
            '/prewarmYTM.view',
        );
        url.searchParams.set('id', id);
        return url.toString();
    } catch {
        return null;
    }
}

export function usePrewarm(streamUrl: string | undefined): void {
    const warmed = useRef('');

    useEffect(() => {
        if (!streamUrl) {
            return;
        }

        const prewarmUrl = prewarmUrlFromStreamUrl(streamUrl);
        if (!prewarmUrl || warmed.current === prewarmUrl) {
            return;
        }

        // Fire-and-forget: the server answers 204 immediately and does the
        // warming in the background. Failures are harmless (playback just
        // falls back to the cold path), so errors are swallowed.
        warmed.current = prewarmUrl;
        fetch(prewarmUrl, { keepalive: true, method: 'GET' }).catch(() => {});
    }, [streamUrl]);
}
