import { RiDiscordLine, RiGlobalLine, RiMailLine } from 'react-icons/ri';

import { AnimatedPage } from '/@/renderer/features/shared/components/animated-page';
import { Button } from '/@/shared/components/button/button';
import { Center } from '/@/shared/components/center/center';
import { Stack } from '/@/shared/components/stack/stack';
import { Text } from '/@/shared/components/text/text';

const SOCIALS = [
    {
        href: 'https://discord.gg/Jn32BrGdYH',
        icon: <RiDiscordLine />,
        label: 'Join our Discord',
    },
    {
        href: 'https://www.uzif.net/',
        icon: <RiGlobalLine />,
        label: "Creator's website",
    },
    {
        href: 'mailto:chroma@haxs.dev',
        icon: <RiMailLine />,
        label: 'chroma@haxs.dev',
    },
];

const SocialsRoute = () => {
    return (
        <AnimatedPage>
            <Center style={{ height: '100%', width: '100%' }}>
                <Stack
                    align="center"
                    gap="xl"
                    style={{ maxWidth: 480, padding: '2rem', textAlign: 'center' }}
                >
                    <Text
                        fw={700}
                        style={{ fontSize: '1.6rem' }}
                    >
                        Socials
                    </Text>
                    <Text
                        style={{ fontSize: '1rem', lineHeight: 1.7 }}
                        variant="secondary"
                    >
                        Follow Monochroma, get help, or reach out to the creator.
                    </Text>
                    <Stack gap="md" style={{ width: '100%' }}>
                        {SOCIALS.map((social) => (
                            <Button
                                component="a"
                                href={social.href}
                                key={social.href}
                                leftSection={social.icon}
                                rel="noopener noreferrer"
                                size="lg"
                                target="_blank"
                                variant="filled"
                            >
                                {social.label}
                            </Button>
                        ))}
                    </Stack>
                </Stack>
            </Center>
        </AnimatedPage>
    );
};

export default SocialsRoute;
