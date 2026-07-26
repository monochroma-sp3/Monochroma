import { AnimatedPage } from '/@/renderer/features/shared/components/animated-page';
import { Button } from '/@/shared/components/button/button';
import { Center } from '/@/shared/components/center/center';
import { Stack } from '/@/shared/components/stack/stack';
import { Text } from '/@/shared/components/text/text';

const DonateRoute = () => {
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
                        Support Monochroma
                    </Text>
                    <Text
                        style={{ fontSize: '1rem', lineHeight: 1.7 }}
                        variant="secondary"
                    >
                        If Monochroma has been useful to you and you&apos;re able to, consider
                        making a donation. It helps pay for the server and domain, keep the service
                        alive, and you get to support us :)
                    </Text>
                    <Button
                        component="a"
                        href="https://ko-fi.com/monochromacc"
                        rel="noopener noreferrer"
                        size="lg"
                        style={{ marginTop: '0.5rem' }}
                        target="_blank"
                        variant="filled"
                    >
                        donate on ko-fi
                    </Button>
                </Stack>
            </Center>
        </AnimatedPage>
    );
};

export default DonateRoute;
