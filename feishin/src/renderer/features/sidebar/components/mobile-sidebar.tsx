import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import {
    RiCupLine,
    RiDownloadLine,
    RiGroupLine,
    RiInformationLine,
    RiPlayListAddLine,
} from 'react-icons/ri';

import styles from './mobile-sidebar.module.css';

import { ActionBar } from '/@/renderer/features/sidebar/components/action-bar';
import { SidebarIcon } from '/@/renderer/features/sidebar/components/sidebar-icon';
import { SidebarItem } from '/@/renderer/features/sidebar/components/sidebar-item';
import {
    SidebarPlaylistAddDragContext,
    SidebarPlaylistList,
    SidebarSharedPlaylistList,
    useSidebarPlaylistAddDragMonitor,
} from '/@/renderer/features/sidebar/components/sidebar-playlist-list';
import MonochromaLogo from '/@/renderer/features/servers/assets/navidrome.png';
import { AppRoute } from '/@/renderer/router/routes';
import {
    SidebarItemType,
    useSidebarItems,
    useSidebarPlaylistList,
} from '/@/renderer/store/settings.store';
import { Accordion } from '/@/shared/components/accordion/accordion';
import { Group } from '/@/shared/components/group/group';
import { ScrollArea } from '/@/shared/components/scroll-area/scroll-area';
import { Text } from '/@/shared/components/text/text';

const MobileSidebarPlaylistSection = () => {
    const isAddDragActive = useSidebarPlaylistAddDragMonitor();

    return (
        <SidebarPlaylistAddDragContext.Provider value={isAddDragActive}>
            <SidebarPlaylistList />
            <SidebarSharedPlaylistList />
        </SidebarPlaylistAddDragContext.Provider>
    );
};

export const MobileSidebar = () => {
    const { t } = useTranslation();
    const sidebarPlaylistList = useSidebarPlaylistList();

    const translatedSidebarItemMap = useMemo(
        () => ({
            Albums: t('page.sidebar.albums'),
            Artists: t('page.sidebar.albumArtists'),
            'Artists-all': t('page.sidebar.artists'),
            Favorites: t('page.sidebar.favorites'),
            Genres: t('page.sidebar.genres'),
            Home: t('page.sidebar.home'),
            'Now Playing': t('page.sidebar.nowPlaying'),
            Playlists: t('page.sidebar.playlists'),
            Search: t('page.sidebar.search'),
            Settings: t('page.sidebar.settings'),
            Tracks: t('page.sidebar.tracks'),
        }),
        [t],
    );

    const sidebarItems = useSidebarItems();

    const sidebarItemsWithRoute: SidebarItemType[] = useMemo(() => {
        if (!sidebarItems) return [];

        const items = sidebarItems
            .filter((item) => !item.disabled)
            .map((item) => ({
                ...item,
                label:
                    translatedSidebarItemMap[item.id as keyof typeof translatedSidebarItemMap] ??
                    item.label,
            }));

        return items;
    }, [sidebarItems, translatedSidebarItemMap]);

    return (
        <div className={styles.container} id="mobile-sidebar">
            <Group grow id="global-search-container" style={{ flexShrink: 0 }}>
                <ActionBar />
            </Group>
            <Group
                align="center"
                gap="sm"
                style={{
                    flexShrink: 0,
                    padding: '0.25rem 1rem 0.5rem 1rem',
                }}
            >
                <img
                    alt="Monochroma"
                    src={MonochromaLogo}
                    style={{ height: 36, objectFit: 'contain', width: 36 }}
                />
                <Text fw={700} style={{ fontSize: '1.25rem', letterSpacing: '-0.01em' }}>
                    Monochroma
                </Text>
            </Group>
            <ScrollArea allowDragScroll className={styles.scrollArea}>
                <Accordion
                    classNames={{
                        content: styles.accordionContent,
                        control: styles.accordionControl,
                        item: styles.accordionItem,
                        root: styles.accordionRoot,
                    }}
                    defaultValue={['library', 'playlists', 'platform']}
                    multiple
                >
                    <Accordion.Item value="platform">
                        <Accordion.Control>
                            <Text fw={600} variant="secondary">
                                Platform
                            </Text>
                        </Accordion.Control>
                        <Accordion.Panel>
                            {Boolean(window.TRANSFER_URL) && (
                                <SidebarItem to={AppRoute.MONOCHROMA_IMPORT}>
                                    <Group gap="sm">
                                        <RiPlayListAddLine />
                                        Import Playlist
                                    </Group>
                                </SidebarItem>
                            )}
                            <SidebarItem to={AppRoute.MONOCHROMA_DOWNLOAD}>
                                <Group gap="sm">
                                    <RiDownloadLine />
                                    Download APP
                                </Group>
                            </SidebarItem>
                            <SidebarItem to={AppRoute.MONOCHROMA_DONATE}>
                                <Group gap="sm">
                                    <RiCupLine />
                                    Donate
                                </Group>
                            </SidebarItem>
                            <SidebarItem to={AppRoute.MONOCHROMA_SOCIALS}>
                                <Group gap="sm">
                                    <RiGroupLine />
                                    Socials
                                </Group>
                            </SidebarItem>
                            <SidebarItem to={AppRoute.MONOCHROMA_ABOUT}>
                                <Group gap="sm">
                                    <RiInformationLine />
                                    About
                                </Group>
                            </SidebarItem>
                        </Accordion.Panel>
                    </Accordion.Item>
                    <Accordion.Item value="library">
                        <Accordion.Control>
                            <Text fw={600} variant="secondary">
                                {t('page.sidebar.myLibrary')}
                            </Text>
                        </Accordion.Control>
                        <Accordion.Panel>
                            {sidebarItemsWithRoute.map((item) => {
                                return (
                                    <SidebarItem key={`sidebar-${item.route}`} to={item.route}>
                                        <Group gap="sm">
                                            <SidebarIcon route={item.route} />
                                            {item.label}
                                        </Group>
                                    </SidebarItem>
                                );
                            })}
                        </Accordion.Panel>
                    </Accordion.Item>
                    {sidebarPlaylistList && <MobileSidebarPlaylistSection />}
                </Accordion>
            </ScrollArea>
        </div>
    );
};
