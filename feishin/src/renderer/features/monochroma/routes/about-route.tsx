import MonochromaLogo from '/@/renderer/features/servers/assets/monochroma-logo.png';
import { AnimatedPage } from '/@/renderer/features/shared/components/animated-page';
import { Center } from '/@/shared/components/center/center';
import { Divider } from '/@/shared/components/divider/divider';
import { Group } from '/@/shared/components/group/group';
import { Stack } from '/@/shared/components/stack/stack';
import { Text } from '/@/shared/components/text/text';

const linkStyle: React.CSSProperties = {
    color: 'var(--theme-colors-primary)',
    textDecoration: 'underline',
};

const AboutRoute = () => {
    return (
        <AnimatedPage>
            <Center style={{ height: '100%', width: '100%' }}>
                <Stack
                    align="center"
                    gap="xl"
                    style={{ maxWidth: 560, padding: '2rem', textAlign: 'center' }}
                >
                    <Group align="center" gap="md" justify="center">
                        <img
                            alt="Monochroma"
                            src={MonochromaLogo}
                            style={{ height: 72, objectFit: 'contain', width: 72 }}
                        />
                        <Text fw={800} style={{ fontSize: '2.5rem', letterSpacing: '-0.02em' }}>
                            Monochroma
                        </Text>
                    </Group>

                    <Text style={{ fontSize: '1.1rem' }} variant="secondary">
                        A new way to experience music.
                    </Text>

                    <Text style={{ fontSize: '1rem', lineHeight: 1.7 }} variant="secondary">
                        Inspired by{' '}
                        <a
                            href="https://mono.haxs.dev/resources/blog/thankyou"
                            rel="noopener noreferrer"
                            style={linkStyle}
                            target="_blank"
                        >
                            Monochrome.tf
                        </a>
                        , Monochroma is a Subsonic server that adds Tidal streaming as a
                        seamlessly integrated virtual library. Imagine Spotify Premium, right?
                        yeah, Monochroma is like that, but free from corporate greed and slop.
                    </Text>

                    <Text style={{ fontSize: '1rem' }}>
                        Our website:{' '}
                        <a
                            href="https://mono.haxs.dev/"
                            rel="noopener noreferrer"
                            style={linkStyle}
                            target="_blank"
                        >
                            https://mono.haxs.dev/
                        </a>
                    </Text>

                    <Text style={{ fontSize: '1rem', lineHeight: 1.7 }} variant="secondary">
                        If you like Monochroma and you&apos;re financially able to, consider
                        making a donation. It helps paying the server fees and will let this
                        project live longer, and you get to support me!
                    </Text>

                    <Text style={{ fontSize: '1rem' }}>
                        made with ❤︎ by{' '}
                        <a
                            href="https://uzif.net"
                            rel="noopener noreferrer"
                            style={linkStyle}
                            target="_blank"
                        >
                            Chroma
                        </a>
                    </Text>

                    <Divider style={{ width: '100%' }} />

                    <Stack gap={4} style={{ width: '100%' }}>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            Nothing about this project is related to any brand.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            Spotify is a registered trademark of Spotify AB.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            iPhone is an Apple product, a registered trademark of Apple Inc.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            This is an independent client and is not affiliated with or endorsed
                            by Tidal or any music streaming service.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            This application does not host, store, or distribute any media files.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            All content displayed or accessed through this app is provided by
                            third-party services and APIs that are publicly available on the
                            internet.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            I do not control, operate, or maintain any external content sources
                            and are not responsible for the accuracy, availability, or legality of
                            any content provided by third parties.
                        </Text>
                        <Text isMuted style={{ fontSize: '0.75rem', lineHeight: 1.6 }}>
                            Users are solely responsible for how they use this application and
                            for ensuring that their use complies with applicable laws and
                            regulations in their jurisdiction.
                        </Text>
                    </Stack>
                </Stack>
            </Center>
        </AnimatedPage>
    );
};

export default AboutRoute;
