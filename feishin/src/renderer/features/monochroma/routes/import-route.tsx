import { useEffect, useRef, useState } from 'react';

import { AnimatedPage } from '/@/renderer/features/shared/components/animated-page';
import { useCurrentServerWithCredential } from '/@/renderer/store';
import { Button } from '/@/shared/components/button/button';
import { Center } from '/@/shared/components/center/center';
import { FileInput } from '/@/shared/components/file-input/file-input';
import { Progress } from '/@/shared/components/progress/progress';
import { ScrollArea } from '/@/shared/components/scroll-area/scroll-area';
import { Stack } from '/@/shared/components/stack/stack';
import { Tabs } from '/@/shared/components/tabs/tabs';
import { Text } from '/@/shared/components/text/text';
import { TextInput } from '/@/shared/components/text-input/text-input';
import { toast } from '/@/shared/components/toast/toast';

// The nd-tools (plim) importer service URL, injected by the server via
// settings.js. The import runs with the already-authenticated session: the
// Navidrome credential string ("u=...&s=...&t=...") holds the Subsonic
// token+salt pair, which nd-tools accepts in place of a password.
const importerBase = () => (window.TRANSFER_URL || '').split('?')[0].replace(/\/$/, '');

const parseCredential = (credential?: string) => {
    const params = new URLSearchParams(credential ?? '');
    return {
        salt: params.get('s') ?? '',
        token: params.get('t') ?? '',
        user: params.get('u') ?? '',
    };
};

type LineKind = 'fail' | 'info' | 'ok' | 'warn';

type ProgressLine = { kind: LineKind; text: string };

const LINE_COLORS: Record<LineKind, string | undefined> = {
    fail: '#f87171',
    info: undefined,
    ok: '#34d399',
    warn: '#fbbf24',
};

const lineKind = (detail: string): LineKind => {
    if (detail.startsWith('✓')) return 'ok';
    if (detail.startsWith('✗')) return 'fail';
    if (detail.startsWith('⚠')) return 'warn';
    return 'info';
};

const ImportRoute = () => {
    const server = useCurrentServerWithCredential();

    const [activeTab, setActiveTab] = useState<null | string>('spotify');
    const [spotifyFile, setSpotifyFile] = useState<File | null>(null);
    const [spotifyName, setSpotifyName] = useState('');
    const [appleFile, setAppleFile] = useState<File | null>(null);
    const [appleName, setAppleName] = useState('');
    const [ytUrl, setYtUrl] = useState('');
    const [ytName, setYtName] = useState('');

    const [isImporting, setIsImporting] = useState(false);
    const [percent, setPercent] = useState(0);
    const [showProgress, setShowProgress] = useState(false);
    const [lines, setLines] = useState<ProgressLine[]>([]);
    const [result, setResult] = useState<null | { ok: boolean; text: string }>(null);

    const logRef = useRef<HTMLDivElement | null>(null);
    const unmountedRef = useRef(false);

    useEffect(() => {
        unmountedRef.current = false;
        return () => {
            unmountedRef.current = true;
        };
    }, []);

    useEffect(() => {
        logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
    }, [lines]);

    const base = importerBase();

    const streamJob = async (jobId: string) => {
        let offset = 0;
        let finished = false;

        while (!finished && !unmountedRef.current) {
            try {
                const res = await fetch(`${base}/job/${jobId}?from=${offset}`);
                if (!res.ok || !res.body) {
                    setResult({ ok: false, text: 'Import job not found or expired.' });
                    return;
                }

                const reader = res.body.getReader();
                const decoder = new TextDecoder();
                let buffer = '';

                for (;;) {
                    const { done, value } = await reader.read();
                    if (done) break;
                    buffer += decoder.decode(value, { stream: true });

                    const chunks = buffer.split('\n');
                    buffer = chunks.pop() ?? '';

                    for (const chunk of chunks) {
                        if (!chunk.trim()) continue;
                        let data;
                        try {
                            data = JSON.parse(chunk);
                        } catch {
                            continue;
                        }

                        if (data.type === 'ping') continue;
                        offset += 1;

                        if (data.type === 'progress') {
                            setPercent(Math.min(100, Math.round((data.current / data.total) * 100)));
                            if (data.detail) {
                                setLines((prev) => [
                                    ...prev,
                                    { kind: lineKind(data.detail), text: data.detail },
                                ]);
                            }
                        } else if (data.type === 'info') {
                            setLines((prev) => [...prev, { kind: 'info', text: data.message }]);
                        } else if (data.type === 'done') {
                            setResult({ ok: true, text: data.message || 'Import completed!' });
                            finished = true;
                        } else if (data.type === 'error') {
                            setResult({ ok: false, text: data.error || 'Import failed.' });
                            finished = true;
                        }
                    }

                    if (finished || unmountedRef.current) {
                        await reader.cancel();
                        break;
                    }
                }
            } catch {
                if (!finished && !unmountedRef.current) {
                    setLines((prev) => [
                        ...prev,
                        { kind: 'warn', text: '⚠ Connection interrupted — reconnecting…' },
                    ]);
                    await new Promise((resolve) => setTimeout(resolve, 3000));
                }
            }
        }
    };

    const handleImport = async () => {
        if (!base) {
            toast.error({ message: 'The playlist importer is not configured.' });
            return;
        }

        const { salt, token, user } = parseCredential(server?.credential);
        if (!user || !token || !salt) {
            toast.error({ message: 'No active session found. Please log in again.' });
            return;
        }

        const formData = new FormData();
        formData.append('nd_user', user);
        formData.append('nd_token', token);
        formData.append('nd_salt', salt);

        if (activeTab === 'spotify') {
            if (!spotifyFile) {
                toast.error({ message: 'Select a CSV file.' });
                return;
            }
            if (!spotifyName.trim()) {
                toast.error({ message: 'Enter a name for the playlist.' });
                return;
            }
            formData.append('source', 'spotify');
            formData.append('file', spotifyFile);
            formData.append('playlist_name', spotifyName.trim());
        } else if (activeTab === 'apple') {
            if (!appleFile) {
                toast.error({ message: 'Select a CSV file.' });
                return;
            }
            if (!appleName.trim()) {
                toast.error({ message: 'Enter a name for the playlist.' });
                return;
            }
            formData.append('source', 'apple');
            formData.append('file', appleFile);
            formData.append('playlist_name', appleName.trim());
        } else {
            if (!ytUrl.trim()) {
                toast.error({ message: 'Enter the playlist link.' });
                return;
            }
            formData.append('source', 'youtube');
            formData.append('yt_url', ytUrl.trim());
            formData.append('playlist_name', ytName.trim());
        }

        setIsImporting(true);
        setResult(null);
        setLines([]);
        setPercent(0);
        setShowProgress(true);

        try {
            const res = await fetch(base, { body: formData, method: 'POST' });
            if (!res.ok) {
                let message = 'Error starting the import.';
                try {
                    message = (await res.json()).error || message;
                } catch {
                    // use default message
                }
                setResult({ ok: false, text: message });
                return;
            }
            const { job_id: jobId } = await res.json();
            await streamJob(jobId);
        } catch {
            setResult({ ok: false, text: 'Cannot reach the playlist importer. Try again.' });
        } finally {
            if (!unmountedRef.current) {
                setIsImporting(false);
            }
        }
    };

    return (
        <AnimatedPage>
            <ScrollArea style={{ height: '100%', width: '100%' }}>
                <Center style={{ minHeight: '100%', padding: '2rem 1rem', width: '100%' }}>
                    <Stack gap="lg" style={{ maxWidth: 560, width: '100%' }}>
                        <Stack gap={4}>
                            <Text fw={700} style={{ fontSize: '1.6rem' }}>
                                Import Playlist
                            </Text>
                            <Text variant="secondary">
                                Bring your playlists from other platforms into your Monochroma
                                account.
                            </Text>
                        </Stack>

                        <Tabs onChange={setActiveTab} value={activeTab}>
                            <Tabs.List>
                                <Tabs.Tab value="spotify">Spotify</Tabs.Tab>
                                <Tabs.Tab value="apple">Apple Music</Tabs.Tab>
                                <Tabs.Tab value="youtube">YouTube</Tabs.Tab>
                            </Tabs.List>
                            <Tabs.Panel value="spotify">
                                <Stack gap="md" pt="lg">
                                    <Text fz="sm" variant="secondary">
                                        Export your playlist from Spotify using{' '}
                                        <a
                                            href="https://exportify.net"
                                            rel="noopener noreferrer"
                                            style={{ color: 'var(--theme-colors-primary)' }}
                                            target="_blank"
                                        >
                                            Exportify
                                        </a>
                                        , then upload the CSV file below.
                                    </Text>
                                    <FileInput
                                        accept=".csv"
                                        clearable
                                        label="CSV file (Exportify)"
                                        onChange={setSpotifyFile}
                                        placeholder="Select file…"
                                        value={spotifyFile}
                                    />
                                    <TextInput
                                        label="Playlist name"
                                        onChange={(e) => setSpotifyName(e.currentTarget.value)}
                                        placeholder="e.g. Svmmer Vibes✨🤍"
                                        value={spotifyName}
                                    />
                                </Stack>
                            </Tabs.Panel>
                            <Tabs.Panel value="apple">
                                <Stack gap="md" pt="lg">
                                    <Text fz="sm" variant="secondary">
                                        Export your playlist from Apple Music using{' '}
                                        <a
                                            href="https://www.tunemymusic.com"
                                            rel="noopener noreferrer"
                                            style={{ color: 'var(--theme-colors-primary)' }}
                                            target="_blank"
                                        >
                                            TuneMyMusic
                                        </a>
                                        , then upload the CSV file below.
                                    </Text>
                                    <FileInput
                                        accept=".csv"
                                        clearable
                                        label="CSV file (TuneMyMusic)"
                                        onChange={setAppleFile}
                                        placeholder="Select file…"
                                        value={appleFile}
                                    />
                                    <TextInput
                                        label="Playlist name"
                                        onChange={(e) => setAppleName(e.currentTarget.value)}
                                        placeholder="e.g. Favs 2026"
                                        value={appleName}
                                    />
                                </Stack>
                            </Tabs.Panel>
                            <Tabs.Panel value="youtube">
                                <Stack gap="md" pt="lg">
                                    <Text fz="sm" variant="secondary">
                                        Paste the link of a public playlist from YouTube or YouTube
                                        Music.
                                    </Text>
                                    <TextInput
                                        label="Playlist link"
                                        onChange={(e) => setYtUrl(e.currentTarget.value)}
                                        placeholder="https://www.youtube.com/playlist?list=…"
                                        value={ytUrl}
                                    />
                                    <TextInput
                                        label="Playlist name (optional)"
                                        onChange={(e) => setYtName(e.currentTarget.value)}
                                        placeholder="Original name will be used if left empty"
                                        value={ytName}
                                    />
                                </Stack>
                            </Tabs.Panel>
                        </Tabs>

                        <Button
                            loading={isImporting}
                            onClick={handleImport}
                            size="md"
                            variant="filled"
                        >
                            Import playlist
                        </Button>

                        {result && (
                            <Text
                                style={{ color: result.ok ? '#34d399' : '#f87171' }}
                            >
                                {result.text}
                            </Text>
                        )}

                        {showProgress && (
                            <Stack gap="sm">
                                <Progress value={percent} />
                                {lines.length > 0 && (
                                    <div
                                        ref={logRef}
                                        style={{
                                            background: 'var(--theme-colors-surface)',
                                            border: '1px solid var(--theme-colors-border, transparent)',
                                            borderRadius: 'var(--theme-card-default-radius)',
                                            maxHeight: 240,
                                            overflowY: 'auto',
                                            padding: '0.75rem 1rem',
                                        }}
                                    >
                                        {lines.map((line, index) => (
                                            <Text
                                                fz="sm"
                                                key={index}
                                                style={{
                                                    color: LINE_COLORS[line.kind],
                                                    padding: '2px 0',
                                                }}
                                                variant={
                                                    line.kind === 'info' ? 'secondary' : undefined
                                                }
                                            >
                                                {line.text}
                                            </Text>
                                        ))}
                                    </div>
                                )}
                            </Stack>
                        )}
                    </Stack>
                </Center>
            </ScrollArea>
        </AnimatedPage>
    );
};

export default ImportRoute;
