import { RiDownloadLine } from 'react-icons/ri';

import { AnimatedPage } from '/@/renderer/features/shared/components/animated-page';
import { Button } from '/@/shared/components/button/button';
import { Center } from '/@/shared/components/center/center';
import { Stack } from '/@/shared/components/stack/stack';
import { Text } from '/@/shared/components/text/text';

const DownloadRoute = () => {
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
                        Download Monochroma
                    </Text>
                    <Text
                        style={{ fontSize: '1rem', lineHeight: 1.7 }}
                        variant="secondary"
                    >
                        Get Monochroma on Android, iPhone, or your computer. Follow the install
                        guide to set it up in minutes.
                    </Text>
                    <Button
                        component="a"
                        href="https://mono.haxs.dev/install"
                        leftSection={<RiDownloadLine />}
                        rel="noopener noreferrer"
                        size="lg"
                        style={{ marginTop: '0.5rem' }}
                        target="_blank"
                        variant="filled"
                    >
                        Open install guide
                    </Button>
                </Stack>
            </Center>
        </AnimatedPage>
    );
};

export default DownloadRoute;
