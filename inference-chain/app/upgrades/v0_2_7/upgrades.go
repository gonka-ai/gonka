package v0_2_7

import (
	"context"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type BountyReward struct {
	Address string
	Amount  int64
}

var (

	// Reward for Epoch 117
	epoch117Rewards = []BountyReward{
		// 9_286_463_707_944 + 10_059_817_523_652 (epoch + additional)
		{
			Address: "gonka1v5ggga7lslfg2e57m9anxud40v2s4t9dw8yj68",
			Amount:  19_346_281_231_596,
		},
		// 8_692_322_398_536 + 9_416_197_589_963 (epoch + additional)
		{
			Address: "gonka145qtr0h90fvz88klshvl4r4htw4thdgcvpey3g",
			Amount:  18_108_519_988_499,
		},
		// 13_252_784_992_124 (additional only, already claimed)
		{
			Address: "gonka1w6wwv3wq25p8qge4lqsnfzs8lsd3s8ty6au65p",
			Amount:  13_252_784_992_124,
		},
		// 8_347_948_194_227 (additional only, already claimed)
		{
			Address: "gonka19ne8zk9j5xk50zvwcpwyeyl2wn72xmwkfycnse",
			Amount:  8_347_948_194_227,
		},
		// 7_941_709_743_097 (additional only, already claimed)
		{
			Address: "gonka127ut4ut239agy405vmznj2jz3z57egrfuza7dw",
			Amount:  7_941_709_743_097,
		},
		// 7_176_093_922_038 (additional only, already claimed)
		{
			Address: "gonka1p2959dx973hd57qsalxvesrcv649296x90ry76",
			Amount:  7_176_093_922_038,
		},
		// 6_460_284_361_863 (additional only, already claimed)
		{
			Address: "gonka1yn54kaefrf7sjk66c74gqap0mquzjqvthsyhwe",
			Amount:  6_460_284_361_863,
		},
		// 6_260_077_591_765 (additional only, already claimed)
		{
			Address: "gonka14ayaxyz859v9vdjvv6e5h06hvnzhxtn6kwj59y",
			Amount:  6_260_077_591_765,
		},
		// 6_202_548_415_263 (additional only, already claimed)
		{
			Address: "gonka1aun6f73uq2r5fujk38xe0tww980r6a0lz6a45g",
			Amount:  6_202_548_415_263,
		},
		// 6_190_505_902_775 (additional only, already claimed)
		{
			Address: "gonka1kyr0zzypa62nk9q2hg9pw7e6q626m83m5td45e",
			Amount:  6_190_505_902_775,
		},
		// 5_892_388_270_281 (additional only, already claimed)
		{
			Address: "gonka1y95qr73kms3zv5ju0kxtu58ksxdyrkyz8m0430",
			Amount:  5_892_388_270_281,
		},
		// 5_690_283_495_497 (additional only, already claimed)
		{
			Address: "gonka155cnj622zfdl64f23ljmk2tzv7tewl6fp4m2hl",
			Amount:  5_690_283_495_497,
		},
		// 5_404_666_514_551 (additional only, already claimed)
		{
			Address: "gonka1eqjy6gy8vapf3xtukyrcva0cn952m07sntnlzv",
			Amount:  5_404_666_514_551,
		},
		// 4_681_134_038_760 (additional only, already claimed)
		{
			Address: "gonka1apw5tzk6a3l9hdpdx5q9v2leehvz5rvvw44x8s",
			Amount:  4_681_134_038_760,
		},
		// 4_511_229_895_191 (additional only, already claimed)
		{
			Address: "gonka14ued4vcdeluj9v9vmsmteap7vtg7t50640hvmf",
			Amount:  4_511_229_895_191,
		},
		// 4_276_400_901_691 (additional only, already claimed)
		{
			Address: "gonka13x8n3ttss76js4f86maeck6uey5nklvy6lvdnw",
			Amount:  4_276_400_901_691,
		},
		// 4_042_749_980_065 (additional only, already claimed)
		{
			Address: "gonka1l0jr5p0nuyxu2ed3xlwnwpz3alqf2hg5fv20vj",
			Amount:  4_042_749_980_065,
		},
		// 4_026_322_422_270 (additional only, already claimed)
		{
			Address: "gonka1d249js0g2tlnk507jcrhtk7l948ylg4a45xnm0",
			Amount:  4_026_322_422_270,
		},
		// 4_010_811_142_599 (additional only, already claimed)
		{
			Address: "gonka1tdu4zkr4lldjssencfagv2azcw9uwy4yralmem",
			Amount:  4_010_811_142_599,
		},
		// 3_929_851_425_497 (additional only, already claimed)
		{
			Address: "gonka19fpma3577v3fnk8nxjkvg442ss8hvglxwqgzz6",
			Amount:  3_929_851_425_497,
		},
		// 3_740_181_853_825 (additional only, already claimed)
		{
			Address: "gonka1vjshzxh3sfam2xh0f7vzz4klrv5pkq4zutk8qt",
			Amount:  3_740_181_853_825,
		},
		// 3_680_165_636_702 (additional only, already claimed)
		{
			Address: "gonka1r34p353cxrvxf3x29raz0x8axflen82a04env4",
			Amount:  3_680_165_636_702,
		},
		// 3_457_968_191_625 (additional only, already claimed)
		{
			Address: "gonka1k44asc7sg45hufxyg6jrg9melfcn8wqu9u8rzm",
			Amount:  3_457_968_191_625,
		},
		// 3_452_732_316_631 (additional only, already claimed)
		{
			Address: "gonka167m8t885t8j4wr8qmh652x9d2jvhtdk4f367fd",
			Amount:  3_452_732_316_631,
		},
		// 3_402_271_571_373 (additional only, already claimed)
		{
			Address: "gonka12pwvrkm7vqkme62y0u6pgms79w8eqyt57s2vcx",
			Amount:  3_402_271_571_373,
		},
		// 3_263_913_574_645 (additional only, already claimed)
		{
			Address: "gonka14cu38xpsd8pz5zdkkzwf0jwtpc0vv309ake364",
			Amount:  3_263_913_574_645,
		},
		// 3_192_378_432_534 (additional only, already claimed)
		{
			Address: "gonka1p6wekfevflq2h4rx9jekc86qaqa4ussw8legsd",
			Amount:  3_192_378_432_534,
		},
		// 3_192_051_190_346 (additional only, already claimed)
		{
			Address: "gonka1j4gwddnpf9h6n6899znss7tprqzxpwpedk7ayf",
			Amount:  3_192_051_190_346,
		},
		// 3_149_117_015_393 (additional only, already claimed)
		{
			Address: "gonka18qd8fhk9uj0zk5xlgnsfkpj78ed65sptf0jkwr",
			Amount:  3_149_117_015_393,
		},
		// 3_117_898_110_739 (additional only, already claimed)
		{
			Address: "gonka1338kw8rwv6qlujzrgfacce3ej6emekzjwudrge",
			Amount:  3_117_898_110_739,
		},
		// 2_938_765_737_492 (additional only, already claimed)
		{
			Address: "gonka1sszyf9vva7xvyk84fnc7zwk0gde3avrhstp8yq",
			Amount:  2_938_765_737_492,
		},
		// 2_825_016_353_238 (additional only, already claimed)
		{
			Address: "gonka1n3hh2ysxeg37hus7txur32qvxnkmx20fpqfqyr",
			Amount:  2_825_016_353_238,
		},
		// 2_737_380_895_518 (additional only, already claimed)
		{
			Address: "gonka1hec2h63xkf9qf7gn07uwucveuhjfrqks8f4dmh",
			Amount:  2_737_380_895_518,
		},
		// 2_707_929_098_675 (additional only, already claimed)
		{
			Address: "gonka17rfqamx8vr4zpd6z0jnulre4acht2lj7tq205e",
			Amount:  2_707_929_098_675,
		},
		// 2_659_366_358_102 (additional only, already claimed)
		{
			Address: "gonka1z4ldfav9tl7x3w9aqfry89zd0kt7sa2lhff6te",
			Amount:  2_659_366_358_102,
		},
		// 2_583_511_619_120 (additional only, already claimed)
		{
			Address: "gonka14a7gpyhushhjhdmwn6dw45tfypaxyeks2kpqtd",
			Amount:  2_583_511_619_120,
		},
		// 2_485_600_756_725 (additional only, already claimed)
		{
			Address: "gonka1z7h4mzz5kkcydj4l6lzr9j73x49dlcq84mmkrv",
			Amount:  2_485_600_756_725,
		},
		// 2_425_584_539_601 (additional only, already claimed)
		{
			Address: "gonka1wn9aqvsgnl7zdf4qqndczawav7a22gtq23qax0",
			Amount:  2_425_584_539_601,
		},
		// 2_291_480_691_306 (additional only, already claimed)
		{
			Address: "gonka1rpf7xk4nkk4qzn4wd373a74rdc8wja79kzxmra",
			Amount:  2_291_480_691_306,
		},
		// 2_256_138_535_094 (additional only, already claimed)
		{
			Address: "gonka1l4vfzkwd555pvxqzr3ksphgwgv8xsh89ytmjp0",
			Amount:  2_256_138_535_094,
		},
		// 2_220_927_275_757 (additional only, already claimed)
		{
			Address: "gonka16rtefgwd6qkz6760eyv53s8cuuu87vy0t7xakt",
			Amount:  2_220_927_275_757,
		},
		// 2_213_204_360_140 (additional only, already claimed)
		{
			Address: "gonka1wd0evpm3u4ugfsr6vsr2jsdfqqmy57pw03l444",
			Amount:  2_213_204_360_140,
		},
		// 2_160_845_610_196 (additional only, already claimed)
		{
			Address: "gonka1ln28jur0gvuwf8frx63lwwagysdf03e8ldayf3",
			Amount:  2_160_845_610_196,
		},
		// 2_145_988_814_899 (additional only, already claimed)
		{
			Address: "gonka1vy396smh98ak3ts4zlthjnsv2ypr845mrfz7x5",
			Amount:  2_145_988_814_899,
		},
		// 2_111_235_694_624 (additional only, already claimed)
		{
			Address: "gonka1llxvtg0657ldmqn4l3t0ag496ff355j5kawagy",
			Amount:  2_111_235_694_624,
		},
		// 2_020_327_815_033 (additional only, already claimed)
		{
			Address: "gonka1myu058axjs62mc3e7na9krwvqpfl9z3gtcw9es",
			Amount:  2_020_327_815_033,
		},
		// 1_944_473_076_052 (additional only, already claimed)
		{
			Address: "gonka17hjy6s7d8u3umauk69wmc7whpckttm2lhg23j7",
			Amount:  1_944_473_076_052,
		},
		// 1_832_883_490_233 (additional only, already claimed)
		{
			Address: "gonka1x2lalvrn6cucvzwfcd5cwwhggfuxqckw8k0zrq",
			Amount:  1_832_883_490_233,
		},
		// 1_644_064_748_247 (additional only, already claimed)
		{
			Address: "gonka18ulrvu3qfvvtq3s079dl2dwql2rev66zmgjk97",
			Amount:  1_644_064_748_247,
		},
		// 1_555_251_218_653 (additional only, already claimed)
		{
			Address: "gonka1vhprg9epy683xghp8ddtdlw2y9cycecmm64tje",
			Amount:  1_555_251_218_653,
		},
		// 1_357_466_040_740 (additional only, already claimed)
		{
			Address: "gonka1nkedq5vefl0xlxf66cgpx5l6eldtjtr3e2q5sp",
			Amount:  1_357_466_040_740,
		},
		// 1_259_816_972_093 (additional only, already claimed)
		{
			Address: "gonka1p22v6ncqsys9fh85gdmkakpe24x2zqfyp60z5q",
			Amount:  1_259_816_972_093,
		},
		// 565_812_280_658 + 612_931_733_227 (epoch + additional)
		{
			Address: "gonka1982cyglwds69k8uvn6xl4aq8jwtx4gm3xnxeay",
			Amount:  1_178_744_013_886,
		},
		// 1_113_408_817_563 (additional only, already claimed)
		{
			Address: "gonka1adacqpq6z6sg9qqsy4f0p02p0sfn2f6n6dldae",
			Amount:  1_113_408_817_563,
		},
		// 1_102_937_067_574 (additional only, already claimed)
		{
			Address: "gonka1tx980hp394dfn80rut6gr3j5yujz7wdl08dups",
			Amount:  1_102_937_067_574,
		},
		// 1_037_095_939_519 (additional only, already claimed)
		{
			Address: "gonka1ym3np7guxart483yfdxnlztuazx22cjt0e4a2p",
			Amount:  1_037_095_939_519,
		},
		// 967_327_905_534 (additional only, already claimed)
		{
			Address: "gonka10000uv63da0ee5drk26erkwrvcmpmj7kg6kw48",
			Amount:  967_327_905_534,
		},
		// 455_160_895_204 + 493_065_573_038 (epoch + additional)
		{
			Address: "gonka1qjpz06uz5myq6dhww8dn9z522u8w8a5xndf9gx",
			Amount:  948_226_468_242,
		},
		// 446_536_423_485 + 483_722_876_565 (epoch + additional)
		{
			Address: "gonka1mdpv608e3ft4mcky9mm9pjr5u6e9ge2mm36v32",
			Amount:  930_259_300_050,
		},
		// 800_172_596_021 (additional only, already claimed)
		{
			Address: "gonka18ee58ff309k3fv2dn9f8zfa9q94huk4ue9y7t4",
			Amount:  800_172_596_021,
		},
		// 774_582_256_986 (additional only, already claimed)
		{
			Address: "gonka100070ewvrcraax995yd78cm0nraryzp4hal3hk",
			Amount:  774_582_256_986,
		},
		// 768_430_103_867 (additional only, already claimed)
		{
			Address: "gonka14tqh62mangwzrma2lgg2dm375rcjzn2ydy8ttm",
			Amount:  768_430_103_867,
		},
		// 716_333_147_673 (additional only, already claimed)
		{
			Address: "gonka1pnfzqvyp6kexsjuc8hpsp36snxfagsl4zdnpnn",
			Amount:  716_333_147_673,
		},
		// 707_235_814_870 (additional only, already claimed)
		{
			Address: "gonka1z8dh5kqdn2nnsg527qawy58ca5fme38xffq7ah",
			Amount:  707_235_814_870,
		},
		// 687_012_247_704 (additional only, already claimed)
		{
			Address: "gonka10000627dkz6nvmf09ctqy073v0fls696uznwgg",
			Amount:  687_012_247_704,
		},
		// 664_759_778_978 (additional only, already claimed)
		{
			Address: "gonka1hfum2nzy80k3uq86nla97a4w423u0zh26axfxt",
			Amount:  664_759_778_978,
		},
		// 661_552_805_544 (additional only, already claimed)
		{
			Address: "gonka1czmu5smv804kq6pqtyvmjxcjjj720mgl4xc3hd",
			Amount:  661_552_805_544,
		},
		// 657_364_105_548 (additional only, already claimed)
		{
			Address: "gonka1cw859nqcd9mg3y3alraluswu55xz9j36evsxd3",
			Amount:  657_364_105_548,
		},
		// 651_735_539_930 (additional only, already claimed)
		{
			Address: "gonka1vwsp87trl60t4g67fa677k7d7jztuc3g4qjc3w",
			Amount:  651_735_539_930,
		},
		// 648_463_118_058 (additional only, already claimed)
		{
			Address: "gonka1shc0ywxd993zz3w3h5xdp5uhgu9rv27ws7mmqx",
			Amount:  648_463_118_058,
		},
		// 647_612_288_371 (additional only, already claimed)
		{
			Address: "gonka1rdarkrtrfnfcvrnf58jyndccqj0r4k630n38rh",
			Amount:  647_612_288_371,
		},
		// 645_845_180_560 (additional only, already claimed)
		{
			Address: "gonka1ffjt0acf87chys5w93wsrdj2s94rrmcq8mt48l",
			Amount:  645_845_180_560,
		},
		// 643_947_175_875 (additional only, already claimed)
		{
			Address: "gonka1apnzzz6wlpevze3vzsmk7n0vp6az5609magdf6",
			Amount:  643_947_175_875,
		},
		// 642_245_516_502 (additional only, already claimed)
		{
			Address: "gonka1ea4hhgnahtu4g0zzpmz2p8elcyx7x99hamk0x4",
			Amount:  642_245_516_502,
		},
		// 636_747_847_758 (additional only, already claimed)
		{
			Address: "gonka13pacjyw9quwfvzllp2h7u27h6f5khqlftw3jmk",
			Amount:  636_747_847_758,
		},
		// 636_289_708_696 (additional only, already claimed)
		{
			Address: "gonka12gwxa8vcvahyd4ygcxp4624ywaw98wp953wuva",
			Amount:  636_289_708_696,
		},
		// 634_457_152_448 (additional only, already claimed)
		{
			Address: "gonka1v425qs8gxupjcw3lqx5fsldtve88vd9gaa7r60",
			Amount:  634_457_152_448,
		},
		// 632_755_493_075 (additional only, already claimed)
		{
			Address: "gonka1dms4wtzer5zjx32c3grc5twksd8kdp0ut952g7",
			Amount:  632_755_493_075,
		},
		// 625_621_613_394 (additional only, already claimed)
		{
			Address: "gonka109g8dnt43nj45xhg83jyjt5f2ywz336w36qzyl",
			Amount:  625_621_613_394,
		},
		// 624_443_541_521 (additional only, already claimed)
		{
			Address: "gonka1caju8tg6yg3wkvryhks57jwd8des6ssypfrhhj",
			Amount:  624_443_541_521,
		},
		// 297_674_825_244 + 322_464_451_218 (epoch + additional)
		{
			Address: "gonka1qtd6sqrfhqlfzl5guvq4rh2jduayekf8sx7vze",
			Amount:  620_139_276_462,
		},
		// 619_731_254_026 (additional only, already claimed)
		{
			Address: "gonka1m08n5646hjpavvmjfarad9kr9pxufe7sfy3v7e",
			Amount:  619_731_254_026,
		},
		// 619_338_563_401 (additional only, already claimed)
		{
			Address: "gonka1pj07u20jn9cx48r0jwen6evz7mrfj75t3argv4",
			Amount:  619_338_563_401,
		},
		// 295_872_182_845 + 320_511_689_202 (epoch + additional)
		{
			Address: "gonka1q9aufvwxg7e36pr83ajfw9u2dtx9fh0de33zmv",
			Amount:  616_383_872_047,
		},
		// 292_645_339_685 + 317_016_122_494 (epoch + additional)
		{
			Address: "gonka1v0uvvhrxje6zqeyqqransjulw8h6wfkhmut5s2",
			Amount:  609_661_462_180,
		},
		// 277_273_832_267 + 300_364_513_815 (epoch + additional)
		{
			Address: "gonka1gr8njjad9rwfmay75xvkr6dmtkky3e3fwceuxq",
			Amount:  577_638_346_083,
		},
		// 275_865_755_252 + 298_839_175_615 (epoch + additional)
		{
			Address: "gonka1f47q2ppldw5f9wkfv7xgwhvmptd27knqrl8xr4",
			Amount:  574_704_930_868,
		},
		// 269_764_088_186 + 292_229_376_750 (epoch + additional)
		{
			Address: "gonka17eha2wcrge8zp36tn85ywgx7dqmwu6hux3wqpu",
			Amount:  561_993_464_936,
		},
		// 557_424_341_592 (additional only, already claimed)
		{
			Address: "gonka100009u7hegukxy5ne3w6ycfleaj7uuvh2juxqd",
			Amount:  557_424_341_592,
		},
		// 257_267_404_675 + 278_692_000_228 (epoch + additional)
		{
			Address: "gonka1756ph2flj5y6kw5xqktfgwj3s2ct54ddz0jj0e",
			Amount:  535_959_404_903,
		},
		// 534_190_146_305 (additional only, already claimed)
		{
			Address: "gonka1ujfwdfe0tq667vt5sz2pus524zpjveedp4tkj2",
			Amount:  534_190_146_305,
		},
		// 530_132_343_184 (additional only, already claimed)
		{
			Address: "gonka1gsarmv9gsc7g9fv9nsqtk6t0d7erxpurf5mvvl",
			Amount:  530_132_343_184,
		},
		// 254_416_214_553 + 275_603_370_018 (epoch + additional)
		{
			Address: "gonka1wg894suuxx088yaw6etlh273y0fk946ygl8srz",
			Amount:  530_019_584_571,
		},
		// 253_101_843_504 + 274_179_541_387 (epoch + additional)
		{
			Address: "gonka19s258ae7sx5t0psuwz9sunlksaglwu6705cpzk",
			Amount:  527_281_384_892,
		},
		// 239_607_772_108 + 259_561_716_974 (epoch + additional)
		{
			Address: "gonka1yt2gm863yknevuu6nq6dzafckpxkjdmz3m0pct",
			Amount:  499_169_489_082,
		},
		// 239_373_092_605 + 259_307_493_941 (epoch + additional)
		{
			Address: "gonka1ya6mzkqvk7ss3l4jnu5fspt5vvzzmnn2ftqvda",
			Amount:  498_680_586_546,
		},
		// 238_317_034_843 + 258_163_490_291 (epoch + additional)
		{
			Address: "gonka1u4zxypjgcr8khlzefwjr0vwdaj2uzruw2cehj3",
			Amount:  496_480_525_135,
		},
		// 237_559_856_476 + 257_343_255_974 (epoch + additional)
		{
			Address: "gonka1eexhw7ks3cd88cyu6qx5zzq3ag484mgw4mzn72",
			Amount:  494_903_112_450,
		},
		// 489_750_657_290 (additional only, already claimed)
		{
			Address: "gonka1vkafyzr5yz5aht6gzapw6lc52h5wwmnrjwvqlz",
			Amount:  489_750_657_290,
		},
		// 229_692_563_125 + 248_820_793_818 (epoch + additional)
		{
			Address: "gonka1lc80tv6k38xc6qagx67wy42mkdl3zplgykhjz3",
			Amount:  478_513_356_943,
		},
		// 476_268_279_179 (additional only, already claimed)
		{
			Address: "gonka1hhx6pvs6n4ehgv4wzz7h9f68t8hm96t9fq0ydd",
			Amount:  476_268_279_179,
		},
		// 467_759_982_313 (additional only, already claimed)
		{
			Address: "gonka15e2swr409v3c4ydpq9ahsn3zuqxyj47axn3lf0",
			Amount:  467_759_982_313,
		},
		// 435_624_799_535 (additional only, already claimed)
		{
			Address: "gonka1py4j23jhz2nah9d8lqpxn2lq6e07lx6e6jmaym",
			Amount:  435_624_799_535,
		},
		// 207_411_746_512 + 224_684_485_698 (epoch + additional)
		{
			Address: "gonka12n7ekj4sg7xd63qe772qpeae59v4zw76t0xpd5",
			Amount:  432_096_232_210,
		},
		// 204_933_875_605 + 222_000_263_808 (epoch + additional)
		{
			Address: "gonka1ycmm7dsxa6sg09x2yg05elta6wwcj3mk6em45l",
			Amount:  426_934_139_413,
		},
		// 202_704_420_331 + 219_585_144_991 (epoch + additional)
		{
			Address: "gonka1060g3x0qxkarfvupav5srccyuht95swyl0kpzc",
			Amount:  422_289_565_323,
		},
		// 202_528_410_704 + 219_394_477_716 (epoch + additional)
		{
			Address: "gonka16g6lr5tzelh4m66gxf5hlvcjf0drcwnyse7yau",
			Amount:  421_922_888_421,
		},
		// 189_973_057_317 + 205_793_545_436 (epoch + additional)
		{
			Address: "gonka1c88tzjv8upxvhexey9u0dhkes8ym07fyywmumt",
			Amount:  395_766_602_754,
		},
		// 390_465_377_707 (additional only, already claimed)
		{
			Address: "gonka1vdw6tst3lelc84garssnnjzx7fjpkja2wzpxe8",
			Amount:  390_465_377_707,
		},
		// 186_804_884_033 + 202_361_534_487 (epoch + additional)
		{
			Address: "gonka139qugrjwz49z4qqjxxdszspd6f5dyhur5sazz0",
			Amount:  389_166_418_520,
		},
		// 182_228_633_733 + 197_404_185_338 (epoch + additional)
		{
			Address: "gonka17le4nn342z6jr8jnlwt2d04sxeuhhm8pwvtv79",
			Amount:  379_632_819_072,
		},
		// 175_833_617_288 + 190_476_607_681 (epoch + additional)
		{
			Address: "gonka1fhwdrlzmynw7lwfgws5fdnazxlwqa2xs5td6xw",
			Amount:  366_310_224_970,
		},
		// 167_596_907_901 + 181_553_965_431 (epoch + additional)
		{
			Address: "gonka1wffadfyz0m0r8rrkgkzqvwq3h60z58t90v2vx7",
			Amount:  349_150_873_332,
		},
		// 346_549_476_192 (additional only, already claimed)
		{
			Address: "gonka1fvw4sad9uqe75tg3q9gaj8rhav7dc454wr2gxe",
			Amount:  346_549_476_192,
		},
		// 334_834_205_892 (additional only, already claimed)
		{
			Address: "gonka1h9axtmze6nqeeh9azggrk0k2qcx8lz2shd67jg",
			Amount:  334_834_205_892,
		},
		// 331_038_196_521 (additional only, already claimed)
		{
			Address: "gonka1mev8s38pz9u55jkg46ql6lw8xlsnvf4ggcefd2",
			Amount:  331_038_196_521,
		},
		// 330_841_851_209 (additional only, already claimed)
		{
			Address: "gonka1zvuzj7ya9zafw309prasd0r4jhykf47mhcpp3x",
			Amount:  330_841_851_209,
		},
		// 328_682_052_774 (additional only, already claimed)
		{
			Address: "gonka1t0gevkpnxe8000ke344zuhf5ljwefmdxnfzp0g",
			Amount:  328_682_052_774,
		},
		// 327_111_290_276 (additional only, already claimed)
		{
			Address: "gonka1cz5pcp5wfnkkahasj9vge9snpvl8ayz5nvu8z5",
			Amount:  327_111_290_276,
		},
		// 326_914_944_963 (additional only, already claimed)
		{
			Address: "gonka1zqxg52qngwtgzgnrvljdwcwt9q0jz9r2djkpyj",
			Amount:  326_914_944_963,
		},
		// 325_605_976_215 (additional only, already claimed)
		{
			Address: "gonka1xuk3ganuhygvpge340sfepuwlr89s50fytlx2p",
			Amount:  325_605_976_215,
		},
		// 325_475_079_340 (additional only, already claimed)
		{
			Address: "gonka1y2e6vzpws7dguken363g9zfs3835l7k3m2p8x0",
			Amount:  325_475_079_340,
		},
		// 324_624_249_653 (additional only, already claimed)
		{
			Address: "gonka14vypx85sx0qd4aajmzklhy6s0ygftf57zafd8a",
			Amount:  324_624_249_653,
		},
		// 324_493_352_778 (additional only, already claimed)
		{
			Address: "gonka1ktl3kkn9l68c9amanu8u4868mcjmtsr5tgzmjk",
			Amount:  324_493_352_778,
		},
		// 324_427_904_340 (additional only, already claimed)
		{
			Address: "gonka122yzg0agje5a4nq8spx2fwe2cye79x7hz3shgl",
			Amount:  324_427_904_340,
		},
		// 324_231_559_028 (additional only, already claimed)
		{
			Address: "gonka19rsdxkmu05suy48sz5ly78djzdprk2x6lwqapl",
			Amount:  324_231_559_028,
		},
		// 323_707_971_529 (additional only, already claimed)
		{
			Address: "gonka1kfsa9q7upj8wnqrkaud755y7w2vwug3pjp832h",
			Amount:  323_707_971_529,
		},
		// 323_707_971_529 (additional only, already claimed)
		{
			Address: "gonka1h6829d8eykrqgev9jr630htpze3j220er9f2ag",
			Amount:  323_707_971_529,
		},
		// 323_446_177_780 (additional only, already claimed)
		{
			Address: "gonka1rs07h66nfduhnf5qg7u968zqumragmmacuand8",
			Amount:  323_446_177_780,
		},
		// 323_315_280_905 (additional only, already claimed)
		{
			Address: "gonka1xlvxvzec4n4xn8mjyj88jrg7zlkmfqy9uglucu",
			Amount:  323_315_280_905,
		},
		// 323_315_280_905 (additional only, already claimed)
		{
			Address: "gonka13vpvregum5amwn0s0qyfac0cdngxc2cq3cp67n",
			Amount:  323_315_280_905,
		},
		// 322_988_038_717 (additional only, already claimed)
		{
			Address: "gonka1kch7y24sjux5s7qd9l9j02qzusk33xeqwyz0kc",
			Amount:  322_988_038_717,
		},
		// 322_857_141_842 (additional only, already claimed)
		{
			Address: "gonka1nluanlq02cg8qrwyqn2qm2zf0mmlyqdevtf66a",
			Amount:  322_857_141_842,
		},
		// 322_857_141_842 (additional only, already claimed)
		{
			Address: "gonka1lnus3x4dy0ze8naktrldft5edx4e36qc53yxjf",
			Amount:  322_857_141_842,
		},
		// 322_529_899_655 (additional only, already claimed)
		{
			Address: "gonka1dn2gwpylkpjmxhgpyp6vfwqp5awydtguck5wc5",
			Amount:  322_529_899_655,
		},
		// 322_464_451_218 (additional only, already claimed)
		{
			Address: "gonka10prje5tj49lek50dpch085f66crl62sut2n0vw",
			Amount:  322_464_451_218,
		},
		// 322_268_105_906 (additional only, already claimed)
		{
			Address: "gonka1362yedf9k0xpce8y30xg4g4e2mmgvstaf856qd",
			Amount:  322_268_105_906,
		},
		// 322_268_105_906 (additional only, already claimed)
		{
			Address: "gonka106wrpdwkuudnxdm0lwzw2zharr8tfx7ug07p9f",
			Amount:  322_268_105_906,
		},
		// 322_071_760_593 (additional only, already claimed)
		{
			Address: "gonka1e9muhlte58rwqrr3493qxcwcj8mqrg5azxa6v4",
			Amount:  322_071_760_593,
		},
		// 322_006_312_156 (additional only, already claimed)
		{
			Address: "gonka1j2g3g844qspgnjjrtmcrt55ssg4h6akdt4f66a",
			Amount:  322_006_312_156,
		},
		// 321_286_379_344 (additional only, already claimed)
		{
			Address: "gonka1yw3ggwwftvp2u3vt9l5hcyqftkd05nu4j3h7sr",
			Amount:  321_286_379_344,
		},
		// 321_155_482_469 (additional only, already claimed)
		{
			Address: "gonka1ta39vdtx4qmlwpvmpjyajn5yulflf0ye2nu4m4",
			Amount:  321_155_482_469,
		},
		// 320_893_688_719 (additional only, already claimed)
		{
			Address: "gonka1pd4dw77pux8rz63v7wqeyx2nlun874e2l4ycve",
			Amount:  320_893_688_719,
		},
		// 320_828_240_282 (additional only, already claimed)
		{
			Address: "gonka1tqr0yuykhd4j4wgzdzng46rd22c9jlfuxkz238",
			Amount:  320_828_240_282,
		},
		// 320_304_652_783 (additional only, already claimed)
		{
			Address: "gonka14q3s4vmmlukl936q7e4asuwjnakv7u59hsvuwm",
			Amount:  320_304_652_783,
		},
		// 320_239_204_345 (additional only, already claimed)
		{
			Address: "gonka1ndga0ugvxa975ftycq908hrscpcnt6enzvrzt8",
			Amount:  320_239_204_345,
		},
		// 320_108_307_471 (additional only, already claimed)
		{
			Address: "gonka1m2rsvln78rv2rt4m0ypvzmg6z9pm803nef55k9",
			Amount:  320_108_307_471,
		},
		// 319_911_962_158 (additional only, already claimed)
		{
			Address: "gonka1neug285zahkpplsfmgrhnkmjjwzalk3z88uekt",
			Amount:  319_911_962_158,
		},
		// 319_715_616_845 (additional only, already claimed)
		{
			Address: "gonka1r4cynx8smndx0vc3j8h0x8ld8h9wyzzs73cxye",
			Amount:  319_715_616_845,
		},
		// 319_650_168_408 (additional only, already claimed)
		{
			Address: "gonka1cx5jhxlg8mwjgl8v50h8ypcyxrz4qyh4ef20g4",
			Amount:  319_650_168_408,
		},
		// 319_650_168_408 (additional only, already claimed)
		{
			Address: "gonka1a2hgufj4f8d307cfa8n4kc8xts56rtnvsxydkz",
			Amount:  319_650_168_408,
		},
		// 319_322_926_221 (additional only, already claimed)
		{
			Address: "gonka10qpuqx5xcp4l4yjfh49gdcedgjsh3eazjm470v",
			Amount:  319_322_926_221,
		},
		// 319_322_926_221 (additional only, already claimed)
		{
			Address: "gonka1rn5jll76utekvhjaxpqa4mwap3twnknnd6fv0c",
			Amount:  319_322_926_221,
		},
		// 319_192_029_346 (additional only, already claimed)
		{
			Address: "gonka1hvat7x6qspwh5vw2pynwcdh57kc6xjl8g67ljx",
			Amount:  319_192_029_346,
		},
		// 319_192_029_346 (additional only, already claimed)
		{
			Address: "gonka1vnmmnq4x9f7d3kume6xq2fh8x3mdye25gclzln",
			Amount:  319_192_029_346,
		},
		// 318_733_890_284 (additional only, already claimed)
		{
			Address: "gonka1cn7jka3lx0ex0dxwdcfeaxkqfjl2as080mx90k",
			Amount:  318_733_890_284,
		},
		// 318_733_890_284 (additional only, already claimed)
		{
			Address: "gonka13uuqfsenjazwu6am5fctzscar364s240zek5st",
			Amount:  318_733_890_284,
		},
		// 318_144_854_348 (additional only, already claimed)
		{
			Address: "gonka1e8d0gt4d3u565208jjn035a6h3nvxzwtc4w8j9",
			Amount:  318_144_854_348,
		},
		// 318_144_854_348 (additional only, already claimed)
		{
			Address: "gonka1sw7d7r42hqwgxz4ln2x4p4pjqxgejwqnxxzj3f",
			Amount:  318_144_854_348,
		},
		// 318_013_957_473 (additional only, already claimed)
		{
			Address: "gonka1u894qk409f4rt7fta590n7fu6szsu6s8nqq0dj",
			Amount:  318_013_957_473,
		},
		// 317_883_060_597 (additional only, already claimed)
		{
			Address: "gonka1xcjc32p5g72gc4tjxsm890star2l7w2g8n67w6",
			Amount:  317_883_060_597,
		},
		// 317_490_369_973 (additional only, already claimed)
		{
			Address: "gonka1mxa996t7rjsuj8l7g82qfwcgy5yed5ckf9lr5k",
			Amount:  317_490_369_973,
		},
		// 317_490_369_973 (additional only, already claimed)
		{
			Address: "gonka1xcvj6xlqy7p7fzyne5jqrnz96fa4xfh35vymwt",
			Amount:  317_490_369_973,
		},
		// 317_424_921_535 (additional only, already claimed)
		{
			Address: "gonka1wpar5p0kmn5cwwn5awzjeycgt6rkr58723fttn",
			Amount:  317_424_921_535,
		},
		// 317_359_473_098 (additional only, already claimed)
		{
			Address: "gonka1mfxj2hdjlhaus0julkl042sque2krql745remr",
			Amount:  317_359_473_098,
		},
		// 317_294_024_661 (additional only, already claimed)
		{
			Address: "gonka185qz338ahgj324udxakau55tk9frygxrs8zduz",
			Amount:  317_294_024_661,
		},
		// 317_228_576_223 (additional only, already claimed)
		{
			Address: "gonka1ntlp35e8cyylh5fjmlzt0qvkhyjszsuwngd6ka",
			Amount:  317_228_576_223,
		},
		// 317_228_576_223 (additional only, already claimed)
		{
			Address: "gonka1jrwj2hqjwy5ulgkpdsdv6gwch7x4alnj88wmm5",
			Amount:  317_228_576_223,
		},
		// 317_097_679_349 (additional only, already claimed)
		{
			Address: "gonka1t4jkhfkwexdz6rm95rdd9qg2905uwd59jc7yyt",
			Amount:  317_097_679_349,
		},
		// 317_032_230_911 (additional only, already claimed)
		{
			Address: "gonka1jvp4r2dx3t0d736fmxy5ncgwp0w3kdy063m5ps",
			Amount:  317_032_230_911,
		},
		// 317_032_230_911 (additional only, already claimed)
		{
			Address: "gonka1x8pupvvcgcn00kguy9umycj98qgqehwlxt6l93",
			Amount:  317_032_230_911,
		},
		// 316_770_437_162 (additional only, already claimed)
		{
			Address: "gonka1pwumgsfldkzspvpun6rgty0evpz47l9mwa79v9",
			Amount:  316_770_437_162,
		},
		// 316_443_194_975 (additional only, already claimed)
		{
			Address: "gonka1j3xajqgg0mu9lvfu320mknu736nl0ltmeeqje7",
			Amount:  316_443_194_975,
		},
		// 315_985_055_912 (additional only, already claimed)
		{
			Address: "gonka1p3jfqqnjuu20zyk5t5d9xhemys2z7zxrln5vv0",
			Amount:  315_985_055_912,
		},
		// 315_985_055_912 (additional only, already claimed)
		{
			Address: "gonka1xrcdrdjy0s0e5nhy6nw3tcm9vrzn347qtct4p2",
			Amount:  315_985_055_912,
		},
		// 315_854_159_037 (additional only, already claimed)
		{
			Address: "gonka1u88dwqlx6ew6mhg0pp3wtjaf9d590w2z8706cu",
			Amount:  315_854_159_037,
		},
		// 315_723_262_162 (additional only, already claimed)
		{
			Address: "gonka1nad4h437q7zl6nmwvuxzh9g73vkm5cdymxvuqp",
			Amount:  315_723_262_162,
		},
		// 315_657_813_725 (additional only, already claimed)
		{
			Address: "gonka1lp3q5zammhv5a7fmn5u58clz5k7wr0205h6llk",
			Amount:  315_657_813_725,
		},
		// 315_657_813_725 (additional only, already claimed)
		{
			Address: "gonka1ut2k5s0jgnwx09nrkzvuprcd58m3afumcv87jm",
			Amount:  315_657_813_725,
		},
		// 315_461_468_413 (additional only, already claimed)
		{
			Address: "gonka1vmhd38lxl68lm76755u4xdxp5wqm646a2hasze",
			Amount:  315_461_468_413,
		},
		// 315_461_468_413 (additional only, already claimed)
		{
			Address: "gonka1rss07hzdm9d3y45fg3s3rnxjgzs26yt0h7khs6",
			Amount:  315_461_468_413,
		},
		// 315_134_226_226 (additional only, already claimed)
		{
			Address: "gonka1cpjcdc97u67m3x35hjj8znuvq37pldfsatmcd6",
			Amount:  315_134_226_226,
		},
		// 314_806_984_039 (additional only, already claimed)
		{
			Address: "gonka1nfammd4gwrde0jhn7w0tfu0228a5jfmst76t34",
			Amount:  314_806_984_039,
		},
		// 314_741_535_601 (additional only, already claimed)
		{
			Address: "gonka1w50e3hn86mt8gmaknecnr4mez0j94vlw04lcjq",
			Amount:  314_741_535_601,
		},
		// 313_694_360_602 (additional only, already claimed)
		{
			Address: "gonka1gg2jwjfjf22gda82gm947mjs8z0gpg9tk2eh3m",
			Amount:  313_694_360_602,
		},
		// 313_367_118_415 (additional only, already claimed)
		{
			Address: "gonka1mklzy8yt48wkk7qrgz485dn995zkvfmjy29nfz",
			Amount:  313_367_118_415,
		},
		// 313_236_221_540 (additional only, already claimed)
		{
			Address: "gonka1v8z57yg47n8h4zveyj4cdpn0ll8k9zpcm7egkv",
			Amount:  313_236_221_540,
		},
		// 312_843_530_916 (additional only, already claimed)
		{
			Address: "gonka1ap3tj4grfc000wqyxw8hna7aa96xkd65gfq4fs",
			Amount:  312_843_530_916,
		},
		// 312_385_391_853 (additional only, already claimed)
		{
			Address: "gonka1l059p34y0lcsuudgs8ln83nt6htjnmjcccqn7v",
			Amount:  312_385_391_853,
		},
		// 311_992_701_229 (additional only, already claimed)
		{
			Address: "gonka16jw23vytrm9cw93s2aea6s4g6f2zkrg6dch6wm",
			Amount:  311_992_701_229,
		},
		// 311_927_252_792 (additional only, already claimed)
		{
			Address: "gonka1mqmvutyh7h87fgw6k246c4q5n0xjjfn3vmqupj",
			Amount:  311_927_252_792,
		},
		// 311_600_010_605 (additional only, already claimed)
		{
			Address: "gonka12chgl62wfzvkvjwj7mz568ct7xyzdaa0ld4d8c",
			Amount:  311_600_010_605,
		},
		// 311_403_665_292 (additional only, already claimed)
		{
			Address: "gonka1scl6c3zk73p920m83r2lndljprz68rjepez6u3",
			Amount:  311_403_665_292,
		},
		// 311_207_319_980 (additional only, already claimed)
		{
			Address: "gonka1y6g6xx6lyp3gdnyq7k9tm9fa7ykzjdx68hyzpx",
			Amount:  311_207_319_980,
		},
		// 310_552_835_605 (additional only, already claimed)
		{
			Address: "gonka1py2eckpy6nqu6uxnxzq8zdfwl533xewpca86sx",
			Amount:  310_552_835_605,
		},
		// 310_356_490_293 (additional only, already claimed)
		{
			Address: "gonka1qlqjyqpmmq4r3fa8tfner44z78728z8ndkgvcc",
			Amount:  310_356_490_293,
		},
		// 310_225_593_418 (additional only, already claimed)
		{
			Address: "gonka1m3hfvjy92ewcvykqkk69j7kgkhffwxw0n8q0an",
			Amount:  310_225_593_418,
		},
		// 309_309_315_294 (additional only, already claimed)
		{
			Address: "gonka1ach2c5kms3ds2728a7dlqwsgddftxljsefxd3u",
			Amount:  309_309_315_294,
		},
		// 308_785_727_795 (additional only, already claimed)
		{
			Address: "gonka1urf86dzpmgx44afh32ug7yy3x6hw86gpm9te00",
			Amount:  308_785_727_795,
		},
		// 308_327_588_733 (additional only, already claimed)
		{
			Address: "gonka1a357vdludl6kpmx0c7cstf3vc0muu7t7ltw8sd",
			Amount:  308_327_588_733,
		},
		// 308_131_243_421 (additional only, already claimed)
		{
			Address: "gonka1zrmra5s53f6pgzxx89znkn5k0duwsksr2xuzke",
			Amount:  308_131_243_421,
		},
		// 307_738_552_796 (additional only, already claimed)
		{
			Address: "gonka15z0gkfpf69u7rvqng8ueqhh9dhx870smxwvuyc",
			Amount:  307_738_552_796,
		},
		// 307_738_552_796 (additional only, already claimed)
		{
			Address: "gonka1gwakaz7dzpay0e4at7jmj0mrnqwjex6kza4dlf",
			Amount:  307_738_552_796,
		},
		// 307_607_655_921 (additional only, already claimed)
		{
			Address: "gonka15nkz4dxslphkty92y0c8m9umule32g9n5e5wvm",
			Amount:  307_607_655_921,
		},
		// 307_345_862_171 (additional only, already claimed)
		{
			Address: "gonka1tpvcpcmmqcku5g53tv2q4wehgqw3nmeq3jk4vn",
			Amount:  307_345_862_171,
		},
		// 307_345_862_171 (additional only, already claimed)
		{
			Address: "gonka1quxrzzy8dwhzpmsdql05nqdzj3u3n0syk4r6pr",
			Amount:  307_345_862_171,
		},
		// 307_214_965_296 (additional only, already claimed)
		{
			Address: "gonka160q92s7d63d3gtcx5lmdxu92uf8fjqut0v622k",
			Amount:  307_214_965_296,
		},
		// 307_214_965_296 (additional only, already claimed)
		{
			Address: "gonka14ntugumwzhdlkc66qlzczdmfa02fcm4pcldtwz",
			Amount:  307_214_965_296,
		},
		// 307_214_965_296 (additional only, already claimed)
		{
			Address: "gonka1l5y7amaux93wgd60wn0446h97ezl3vjd6sy025",
			Amount:  307_214_965_296,
		},
		// 307_018_619_984 (additional only, already claimed)
		{
			Address: "gonka182wjthlm5zdkwpcc3x5cfy83sel392ljwufsz7",
			Amount:  307_018_619_984,
		},
		// 306_560_480_922 (additional only, already claimed)
		{
			Address: "gonka1zzkxnpq2txntwh5eu49jk67x42yh8wystnkamy",
			Amount:  306_560_480_922,
		},
		// 306_364_135_610 (additional only, already claimed)
		{
			Address: "gonka1wcxzxzhwjqzdr3uv6ej73ewxs0zw6dcnxvnr8e",
			Amount:  306_364_135_610,
		},
		// 306_233_238_735 (additional only, already claimed)
		{
			Address: "gonka1e9vdm8ny7saz4n7yx7hng8hvdj0nu85v4edgnr",
			Amount:  306_233_238_735,
		},
		// 305_513_305_923 (additional only, already claimed)
		{
			Address: "gonka1qcamnqy86hqzcatvlgr78mvgmpcc25ms40pdj2",
			Amount:  305_513_305_923,
		},
		// 304_858_821_549 (additional only, already claimed)
		{
			Address: "gonka16cstfwxnuv08zpyalmv38e48crhe8a9pft7etg",
			Amount:  304_858_821_549,
		},
		// 304_727_924_674 (additional only, already claimed)
		{
			Address: "gonka1xycg2mc0ktaz4372q4ln3f0thefk86shhguk0w",
			Amount:  304_727_924_674,
		},
		// 304_269_785_613 (additional only, already claimed)
		{
			Address: "gonka1tvgem50n2xna9apg29wm0ts5rjmjxj0zv23t69",
			Amount:  304_269_785_613,
		},
		// 304_007_991_862 (additional only, already claimed)
		{
			Address: "gonka1d8v8s56a27wrjc0gfj4c0vafq67j2ddhr7n5k7",
			Amount:  304_007_991_862,
		},
		// 303_942_543_425 (additional only, already claimed)
		{
			Address: "gonka1lzxwqhc5fqwltg57emk2lprd7qzmapp22mv0h7",
			Amount:  303_942_543_425,
		},
		// 303_746_198_112 (additional only, already claimed)
		{
			Address: "gonka1hqwgkck88d5wnpea0y9dn0gxmyg4rwjgcnvacf",
			Amount:  303_746_198_112,
		},
		// 303_615_301_238 (additional only, already claimed)
		{
			Address: "gonka17ftklmd8esnj47nr7dg7c5c6wujvqxqjv03774",
			Amount:  303_615_301_238,
		},
		// 303_484_404_363 (additional only, already claimed)
		{
			Address: "gonka148eld5kxu6k867pz6yxmwps0kpy7rdxg2x2xau",
			Amount:  303_484_404_363,
		},
		// 303_418_955_925 (additional only, already claimed)
		{
			Address: "gonka1e6q0zgndr833egu2050lqqdg7tg8av62duv6em",
			Amount:  303_418_955_925,
		},
		// 145_266_612_081 + 157_364_057_597 (epoch + additional)
		{
			Address: "gonka1lxnfxu7t2wwerxhjpaxm0sk5rhd7w99tj8dyy0",
			Amount:  302_630_669_678,
		},
		// 302_109_987_177 (additional only, already claimed)
		{
			Address: "gonka1ylhpz2dd9f6x6apkzr773yw0kaagkhpaph86qn",
			Amount:  302_109_987_177,
		},
		// 302_044_538_739 (additional only, already claimed)
		{
			Address: "gonka1zepcpcnw6qa45k38muhgavzflelun6rh5hu35k",
			Amount:  302_044_538_739,
		},
		// 301_979_090_302 (additional only, already claimed)
		{
			Address: "gonka1clm58yldg7zp7dmuwrqyh4quqt339qr3q8fajm",
			Amount:  301_979_090_302,
		},
		// 301_848_193_427 (additional only, already claimed)
		{
			Address: "gonka1e4qcqggpllj4ulsd97ha9agm2e05sd48d4lmyh",
			Amount:  301_848_193_427,
		},
		// 301_324_605_928 (additional only, already claimed)
		{
			Address: "gonka1fp444xd5027mzhx0tnlxkx6euczhp9glt5f09a",
			Amount:  301_324_605_928,
		},
		// 301_259_157_491 (additional only, already claimed)
		{
			Address: "gonka1zaklf2yedw6my8lw6vt797vzpn67qeusj5a6zl",
			Amount:  301_259_157_491,
		},
		// 300_670_121_553 (additional only, already claimed)
		{
			Address: "gonka1wcqxjxzjsfwvaxl3l24qzemjlf0e8nextxhv8j",
			Amount:  300_670_121_553,
		},
		// 300_604_673_116 (additional only, already claimed)
		{
			Address: "gonka1qhzauckmv78r9wpsr6lvjku5g70hmktgv2ku7d",
			Amount:  300_604_673_116,
		},
		// 300_342_879_366 (additional only, already claimed)
		{
			Address: "gonka1v765pascdp3v6qqtpdrcqm8qwdwq4hxa85gu5z",
			Amount:  300_342_879_366,
		},
		// 300_211_982_491 (additional only, already claimed)
		{
			Address: "gonka1y9vdzmy84uy9c9dgqx8adnusg3ssv7wjmscga8",
			Amount:  300_211_982_491,
		},
		// 299_950_188_742 (additional only, already claimed)
		{
			Address: "gonka15g9vxflt3q92w07kcw099zjtc3lft9vwtr254x",
			Amount:  299_950_188_742,
		},
		// 299_492_049_680 (additional only, already claimed)
		{
			Address: "gonka1dqgcdz94x42ukcfmy6crj55a0j2qucsaecx0j5",
			Amount:  299_492_049_680,
		},
		// 299_295_704_368 (additional only, already claimed)
		{
			Address: "gonka135jr9e2gyzq5lfqvc9tq8n0k3c398dgmmwy835",
			Amount:  299_295_704_368,
		},
		// 299_033_910_618 (additional only, already claimed)
		{
			Address: "gonka10ejjacwvwma37rn8wrf3je7gt8u7lulu7vh8ly",
			Amount:  299_033_910_618,
		},
		// 298_968_462_180 (additional only, already claimed)
		{
			Address: "gonka1r9u4nyprt7ym64pdea4a7wpjf446c0zsmphqx9",
			Amount:  298_968_462_180,
		},
		// 298_968_462_180 (additional only, already claimed)
		{
			Address: "gonka1sn2fuds6qh5dhrdp7afdyepmr6lcpwvsuwzlye",
			Amount:  298_968_462_180,
		},
		// 298_510_323_118 (additional only, already claimed)
		{
			Address: "gonka1thdyumjevc50uf822fzk6ypmychmvsdqu6wnnz",
			Amount:  298_510_323_118,
		},
		// 298_117_632_494 (additional only, already claimed)
		{
			Address: "gonka1ax52nxfepvshvatstyd3t3v56feaph24xzzf79",
			Amount:  298_117_632_494,
		},
		// 298_052_184_056 (additional only, already claimed)
		{
			Address: "gonka1equqcel7hzlu07ekt7tep62yh23wp0cc2ryqyr",
			Amount:  298_052_184_056,
		},
		// 297_855_838_744 (additional only, already claimed)
		{
			Address: "gonka14pdvgt025fwkf38cr74hhcj2nfl98c044tswya",
			Amount:  297_855_838_744,
		},
		// 297_463_148_120 (additional only, already claimed)
		{
			Address: "gonka14r6ypa2jngcd8pwt22f2nwdhlt2s3cxr4k9xne",
			Amount:  297_463_148_120,
		},
		// 297_397_699_682 (additional only, already claimed)
		{
			Address: "gonka1nuhz5kqnmxs3dfd884hdv39kdy2854lff8ma8c",
			Amount:  297_397_699_682,
		},
		// 297_397_699_682 (additional only, already claimed)
		{
			Address: "gonka1djqnccng4mece9p7kpyh588thq6hzt3vk2fmln",
			Amount:  297_397_699_682,
		},
		// 297_266_802_808 (additional only, already claimed)
		{
			Address: "gonka1r4zuhduduwqzyw4a5y5y5aw57sj9jl56qpp7pk",
			Amount:  297_266_802_808,
		},
		// 297_266_802_808 (additional only, already claimed)
		{
			Address: "gonka1k3lva2jsxu5qx30f65yxdlcey8d6f3unmy2gks",
			Amount:  297_266_802_808,
		},
		// 297_201_354_369 (additional only, already claimed)
		{
			Address: "gonka1apdtylf7dfskdy545zv2pxvrfja286vrl7cqh8",
			Amount:  297_201_354_369,
		},
		// 296_874_112_182 (additional only, already claimed)
		{
			Address: "gonka1cze6ecxhaqxmls39l0kwxad50x2skqmk3hswlf",
			Amount:  296_874_112_182,
		},
		// 296_546_869_995 (additional only, already claimed)
		{
			Address: "gonka1hmeg4xq8autummlkzathgxaezmwe02ul2qe0c3",
			Amount:  296_546_869_995,
		},
		// 295_826_937_184 (additional only, already claimed)
		{
			Address: "gonka1cq9f8uc3hz39qtuf3q77r5kv2y2hy52ehmmvy6",
			Amount:  295_826_937_184,
		},
		// 295_761_488_747 (additional only, already claimed)
		{
			Address: "gonka19w5vy2z5gz9f4jr76f8uz09023wz2snycaty05",
			Amount:  295_761_488_747,
		},
		// 295_630_591_872 (additional only, already claimed)
		{
			Address: "gonka10r5p7vd7sw6tthp0nuld8yajvpgufamrd63epk",
			Amount:  295_630_591_872,
		},
		// 295_107_004_372 (additional only, already claimed)
		{
			Address: "gonka1evsnmudxqr9lxchjj5gsxq39qk4p3tr0y9uxu7",
			Amount:  295_107_004_372,
		},
		// 294_583_416_873 (additional only, already claimed)
		{
			Address: "gonka1udx48lyqzrc2ygpkzjq6wthh3ejm6x5cytvxjh",
			Amount:  294_583_416_873,
		},
		// 294_452_519_998 (additional only, already claimed)
		{
			Address: "gonka1cx4dkft2kzdzfjcuale2lw5cqaptdzgahwrj90",
			Amount:  294_452_519_998,
		},
		// 294_387_071_560 (additional only, already claimed)
		{
			Address: "gonka1707yvgk9wgpzrp35wd84mcps4pujelx36n5du9",
			Amount:  294_387_071_560,
		},
		// 294_387_071_560 (additional only, already claimed)
		{
			Address: "gonka1rpszx3te4396yyplpl7lmcqngcwtk5vw0t57ym",
			Amount:  294_387_071_560,
		},
		// 293_994_380_936 (additional only, already claimed)
		{
			Address: "gonka1dr9jslmgk9pp3kt0twe98g3hm5cnn2z3ehpaz2",
			Amount:  293_994_380_936,
		},
		// 293_274_448_124 (additional only, already claimed)
		{
			Address: "gonka1fjrq7m6xy9wpmm88vr63d08ps8dt9em38lhq08",
			Amount:  293_274_448_124,
		},
		// 293_078_102_811 (additional only, already claimed)
		{
			Address: "gonka1xspavhncfmlpsllpfajs3r5623a4s8tcumrmwa",
			Amount:  293_078_102_811,
		},
		// 293_012_654_374 (additional only, already claimed)
		{
			Address: "gonka1nv5twlr9sl69g650apxxe0h4vy7mw6y8cx9sha",
			Amount:  293_012_654_374,
		},
		// 293_012_654_374 (additional only, already claimed)
		{
			Address: "gonka1af4yltv435t3f2mx0qkw2mafdrkmqng05g7l8a",
			Amount:  293_012_654_374,
		},
		// 292_816_309_062 (additional only, already claimed)
		{
			Address: "gonka19h06zf4dmh2qgcdj90cthnw85vpz59cusa72yj",
			Amount:  292_816_309_062,
		},
		// 292_489_066_875 (additional only, already claimed)
		{
			Address: "gonka14hruf6ljltha9yklfgpd0s8jleyc3wdraatfre",
			Amount:  292_489_066_875,
		},
		// 292_423_618_438 (additional only, already claimed)
		{
			Address: "gonka1zkmn95dkr6kpt7vaqc4vdues0q4f72rttcfxq6",
			Amount:  292_423_618_438,
		},
		// 291_834_582_500 (additional only, already claimed)
		{
			Address: "gonka1evnq82x3j2mpf9a7mgrtuav2r2v635q7ekt96g",
			Amount:  291_834_582_500,
		},
		// 291_638_237_188 (additional only, already claimed)
		{
			Address: "gonka1ezpg20aga2mv3avkda2zqh5nh8jys9pg0q2cnq",
			Amount:  291_638_237_188,
		},
		// 291_245_546_564 (additional only, already claimed)
		{
			Address: "gonka1u5t8ue2699e9z92kytnxys2l0eehywsxeqvnnx",
			Amount:  291_245_546_564,
		},
		// 290_852_855_939 (additional only, already claimed)
		{
			Address: "gonka1qx3stahnpknaf8lq0kzdtedtxmq965hj9tp0jv",
			Amount:  290_852_855_939,
		},
		// 290_787_407_502 (additional only, already claimed)
		{
			Address: "gonka15lxz9w7m77dr04dxv7smxmc4xl2vr4klu2gclf",
			Amount:  290_787_407_502,
		},
		// 290_656_510_627 (additional only, already claimed)
		{
			Address: "gonka10gj5lnsxalcwzeve04k2pdzhx2nrlqp4hkrevd",
			Amount:  290_656_510_627,
		},
		// 290_656_510_627 (additional only, already claimed)
		{
			Address: "gonka14m8tpzysrtjyew3s42mxq9u6gxz3pfacafq28k",
			Amount:  290_656_510_627,
		},
		// 290_525_613_752 (additional only, already claimed)
		{
			Address: "gonka13dv9pz037mzr7grelf7035nnanwfazzwydkye6",
			Amount:  290_525_613_752,
		},
		// 290_002_026_252 (additional only, already claimed)
		{
			Address: "gonka1ke4t935aqvya0kcee2nxcjp82vk8sa3sjs60fh",
			Amount:  290_002_026_252,
		},
		// 290_002_026_252 (additional only, already claimed)
		{
			Address: "gonka13qk3jur62xvma480z767lc0ks8c8esc28nxmvt",
			Amount:  290_002_026_252,
		},
		// 289_740_232_502 (additional only, already claimed)
		{
			Address: "gonka1xec9u9kczf7jckvtxgalghsfcnk938h2ng9flq",
			Amount:  289_740_232_502,
		},
		// 289_674_784_065 (additional only, already claimed)
		{
			Address: "gonka1ag6cukxq4nklq309rydjl29q2ptx7xls67trtu",
			Amount:  289_674_784_065,
		},
		// 288_954_851_254 (additional only, already claimed)
		{
			Address: "gonka1dmjrdj39n950jzcxqw6zqa7vhtg945gqu5tfhm",
			Amount:  288_954_851_254,
		},
		// 288_693_057_504 (additional only, already claimed)
		{
			Address: "gonka1c3wplss6ju6fc5mhp0dg4lc7lhvkyrvjg28ejl",
			Amount:  288_693_057_504,
		},
		// 288_300_366_879 (additional only, already claimed)
		{
			Address: "gonka13ykzwj2f5zzngs7jl7ywczgestspevqh2vagsj",
			Amount:  288_300_366_879,
		},
		// 287_842_227_817 (additional only, already claimed)
		{
			Address: "gonka10xrtfzs46mmjs8dy48auh2xfxq07dxs0mhdcmp",
			Amount:  287_842_227_817,
		},
		// 287_580_434_068 (additional only, already claimed)
		{
			Address: "gonka1fzd9ne43l0rjuptdaxdvlhh9x65krl0vucrf5d",
			Amount:  287_580_434_068,
		},
		// 287_384_088_755 (additional only, already claimed)
		{
			Address: "gonka1aevup75mz4q3vd89ljkqjlrkxvfpwk3dtvtvq2",
			Amount:  287_384_088_755,
		},
		// 287_384_088_755 (additional only, already claimed)
		{
			Address: "gonka12fnudtsmw6heu6t982rnk8c5lgpgz4wmvwd75s",
			Amount:  287_384_088_755,
		},
		// 287_122_295_006 (additional only, already claimed)
		{
			Address: "gonka1pla6kzmj079jzaxdk9n3qrkjfdavuwcsmfqchs",
			Amount:  287_122_295_006,
		},
		// 286_598_707_506 (additional only, already claimed)
		{
			Address: "gonka1skdtv4vlkwgcnqel2e48vvquhp40mq83m2yjl7",
			Amount:  286_598_707_506,
		},
		// 286_533_259_068 (additional only, already claimed)
		{
			Address: "gonka1zw680tan4k6uwklyq6eedmga8ew85ys4arqkdg",
			Amount:  286_533_259_068,
		},
		// 286_467_810_631 (additional only, already claimed)
		{
			Address: "gonka1ppqspznyn94e87rxzhnnedc57y4r2hae2g3k6u",
			Amount:  286_467_810_631,
		},
		// 286_206_016_881 (additional only, already claimed)
		{
			Address: "gonka1jmynyvawnx9jlwd6he7q8hdltpy2dv2tz74daz",
			Amount:  286_206_016_881,
		},
		// 285_747_877_820 (additional only, already claimed)
		{
			Address: "gonka1ntsw9ufhpvzan82lhe496zhdqvfy9rptm6rr5s",
			Amount:  285_747_877_820,
		},
		// 285_682_429_382 (additional only, already claimed)
		{
			Address: "gonka1a90lj3ks0fg7mxrvqy7dx4g09g72hrlndpmwsj",
			Amount:  285_682_429_382,
		},
		// 285_486_084_070 (additional only, already claimed)
		{
			Address: "gonka122dtadquy0ayf6nzx7thg6643w7cv0fcnqssq6",
			Amount:  285_486_084_070,
		},
		// 284_962_496_570 (additional only, already claimed)
		{
			Address: "gonka154j3xc2ke2qutupgc97sydt3fwgl6mmkch2k59",
			Amount:  284_962_496_570,
		},
		// 284_635_254_383 (additional only, already claimed)
		{
			Address: "gonka1zh5kme35psmcvlx2tgrg5nzlm0mu0ea3868p67",
			Amount:  284_635_254_383,
		},
		// 284_438_909_071 (additional only, already claimed)
		{
			Address: "gonka1x4wafrl70272pzg3897zzefexwtmeuyar7tsll",
			Amount:  284_438_909_071,
		},
		// 284_177_115_321 (additional only, already claimed)
		{
			Address: "gonka1x9qndq86jt6x8r5pqx4hlx92rl0x3qu2hn2d3r",
			Amount:  284_177_115_321,
		},
		// 283_915_321_572 (additional only, already claimed)
		{
			Address: "gonka1szk5zjwpx7lzq4phdnqepdyfkf7hnmvh8lawdt",
			Amount:  283_915_321_572,
		},
		// 283_915_321_572 (additional only, already claimed)
		{
			Address: "gonka1ah2y5uemn9fs3xtzrm6xur88e75rzdv0gcr842",
			Amount:  283_915_321_572,
		},
		// 283_849_873_134 (additional only, already claimed)
		{
			Address: "gonka1zmx4cwayvhf8hevcu6am0sk8ul5trckc2uftmp",
			Amount:  283_849_873_134,
		},
		// 283_653_527_822 (additional only, already claimed)
		{
			Address: "gonka1agpg82nyn9gh2wu7txegdg3aydfs2ynqq0spd6",
			Amount:  283_653_527_822,
		},
		// 283_391_734_071 (additional only, already claimed)
		{
			Address: "gonka1d3vxnhh9tm8lahmnehhwcuqy7lc72rudn5hfpg",
			Amount:  283_391_734_071,
		},
		// 283_260_837_197 (additional only, already claimed)
		{
			Address: "gonka1ktd5rha9hghm4hzt2eyc8v2p6g66a0zmry07qh",
			Amount:  283_260_837_197,
		},
		// 282_933_595_010 (additional only, already claimed)
		{
			Address: "gonka17mw34tnvce5jg8s2lrn07556guh7yj25dgm4pf",
			Amount:  282_933_595_010,
		},
		// 282_671_801_260 (additional only, already claimed)
		{
			Address: "gonka12c27qus5pw87x4ayaulmcp34q5pkeseettw3nr",
			Amount:  282_671_801_260,
		},
		// 282_410_007_511 (additional only, already claimed)
		{
			Address: "gonka18jd4my9aae5jc3gp8sjkxtt988ctazxwcu00zz",
			Amount:  282_410_007_511,
		},
		// 282_410_007_511 (additional only, already claimed)
		{
			Address: "gonka1qxy42sc078ut52ws06nsa5fyzcfjyx7a56lw84",
			Amount:  282_410_007_511,
		},
		// 282_279_110_636 (additional only, already claimed)
		{
			Address: "gonka10ekkjpyfad6265m2502d4tp3n8yp9n0d7rlzk4",
			Amount:  282_279_110_636,
		},
		// 282_082_765_324 (additional only, already claimed)
		{
			Address: "gonka1rzchltn9xn5ysur4c6x89xvtuuddzccvt8zqrw",
			Amount:  282_082_765_324,
		},
		// 282_082_765_324 (additional only, already claimed)
		{
			Address: "gonka1strn82prjujtuh0dkpt024a4j49qgf37dq83dn",
			Amount:  282_082_765_324,
		},
		// 282_082_765_324 (additional only, already claimed)
		{
			Address: "gonka1ca9pva58lm296uphq5wd2v95h6amfyvvnypxdc",
			Amount:  282_082_765_324,
		},
		// 281_624_626_261 (additional only, already claimed)
		{
			Address: "gonka1usa7alwmkwrvsu22fg4p8k2ftan35glprkkx4z",
			Amount:  281_624_626_261,
		},
		// 281_493_729_386 (additional only, already claimed)
		{
			Address: "gonka14cfcjj5djk4k38zghtmxktqutu00yc7uh6wvg3",
			Amount:  281_493_729_386,
		},
		// 281_362_832_511 (additional only, already claimed)
		{
			Address: "gonka1pzeet74x2wvssdh9t24f258vz426ydu66pcr6f",
			Amount:  281_362_832_511,
		},
		// 280_970_141_887 (additional only, already claimed)
		{
			Address: "gonka1m0ftrwltdf7dntqhx9juwgadzyl2h4m3sm77zj",
			Amount:  280_970_141_887,
		},
		// 280_642_899_700 (additional only, already claimed)
		{
			Address: "gonka1wjxkarkm69harhu6h77n56slpqmjg6ygarf4tl",
			Amount:  280_642_899_700,
		},
		// 280_446_554_388 (additional only, already claimed)
		{
			Address: "gonka1cnnxxev6yaauzarywk6n6vcs0ej0jygwvff9xz",
			Amount:  280_446_554_388,
		},
		// 279_137_585_639 (additional only, already claimed)
		{
			Address: "gonka1yaqxj5hxmme0q7m2wutngne0fdyaajv83p558f",
			Amount:  279_137_585_639,
		},
		// 279_137_585_639 (additional only, already claimed)
		{
			Address: "gonka1uw0kpk6hcmm0sh05l27sq6m5max7pg0n2ptsuk",
			Amount:  279_137_585_639,
		},
		// 278_744_895_015 (additional only, already claimed)
		{
			Address: "gonka1nl4vtgr88sy088djkxsknau5fwnx8akh3ppl6d",
			Amount:  278_744_895_015,
		},
		// 278_548_549_702 (additional only, already claimed)
		{
			Address: "gonka1j9rxapzwm06quxcul5686e7f39g6swhfhwxtwa",
			Amount:  278_548_549_702,
		},
		// 278_483_101_265 (additional only, already claimed)
		{
			Address: "gonka1hy4zasaf3knphe923exxm3vl6vyr8m5k8y8ucs",
			Amount:  278_483_101_265,
		},
		// 278_352_204_390 (additional only, already claimed)
		{
			Address: "gonka1njzjqpx6t4jwe9t0fu8vtzn5cj5xjlaussvctx",
			Amount:  278_352_204_390,
		},
		// 278_155_859_077 (additional only, already claimed)
		{
			Address: "gonka10sy48qz62el4y2srve5hhydgvump8whmn2aqzy",
			Amount:  278_155_859_077,
		},
		// 278_090_410_640 (additional only, already claimed)
		{
			Address: "gonka14nyfdumjvclvd5y0q8yckfa09ncatcrfhnnx87",
			Amount:  278_090_410_640,
		},
		// 277_435_926_266 (additional only, already claimed)
		{
			Address: "gonka12gmwdxjwyqs8y2cxjzrnza8jtf2pyp34udp893",
			Amount:  277_435_926_266,
		},
		// 277_435_926_266 (additional only, already claimed)
		{
			Address: "gonka129wh6ap5m5v0skcxvdcgn5nrfa8d0v5jwfz83p",
			Amount:  277_435_926_266,
		},
		// 276_323_302_829 (additional only, already claimed)
		{
			Address: "gonka1q2z8yy5lj200k3xgk38cqqlnypkqxphu7gr4r3",
			Amount:  276_323_302_829,
		},
		// 276_257_854_392 (additional only, already claimed)
		{
			Address: "gonka1kzhcxdkfdkdyzvl3sdp34yrwkfngdhjwrmua03",
			Amount:  276_257_854_392,
		},
		// 275_865_163_767 (additional only, already claimed)
		{
			Address: "gonka13fpvt243g8sh87ere5avz74qx5qj7vzesr0p4j",
			Amount:  275_865_163_767,
		},
		// 275_537_921_580 (additional only, already claimed)
		{
			Address: "gonka1dxs4az0nz6mxpdcjjtf2fk9fylrsr9f8d3qzfe",
			Amount:  275_537_921_580,
		},
		// 275_210_679_393 (additional only, already claimed)
		{
			Address: "gonka1l5v6g8xahf88pqnv3htq72c6kf792yvw3aqx39",
			Amount:  275_210_679_393,
		},
		// 275_145_230_956 (additional only, already claimed)
		{
			Address: "gonka1p2t9xrr5qs8xxx7rayy37tjh7pvl0x4sdgnd8h",
			Amount:  275_145_230_956,
		},
		// 274_883_437_206 (additional only, already claimed)
		{
			Address: "gonka1y46v988rv03mdn2c320fxtle6lramzk4nf8s6r",
			Amount:  274_883_437_206,
		},
		// 274_817_988_769 (additional only, already claimed)
		{
			Address: "gonka1l42zhzv3hy6d3y88aq37mdgux4u9crd968v9s4",
			Amount:  274_817_988_769,
		},
		// 274_687_091_893 (additional only, already claimed)
		{
			Address: "gonka1frfr2z4lclq0wjqsrstnhm48rmvkmd0tfsssvk",
			Amount:  274_687_091_893,
		},
		// 274_359_849_706 (additional only, already claimed)
		{
			Address: "gonka1n0225yr8w7eqdsw3g4kz45vpnyajy69vv4z678",
			Amount:  274_359_849_706,
		},
		// 274_032_607_519 (additional only, already claimed)
		{
			Address: "gonka1pq5f5c4k8qn7k73qkzza460y435r3dusaxytys",
			Amount:  274_032_607_519,
		},
		// 274_032_607_519 (additional only, already claimed)
		{
			Address: "gonka1pyvqmxs6rkv8sqpax42gkf9ffvlnqjcx0ht0va",
			Amount:  274_032_607_519,
		},
		// 271_545_566_897 (additional only, already claimed)
		{
			Address: "gonka150qqxu2zf0lzc3nngal79h0n5ls6lhszzraytv",
			Amount:  271_545_566_897,
		},
		// 271_414_670_023 (additional only, already claimed)
		{
			Address: "gonka1j4zdyxve2f9fkj8d2nx2cvg09wv67cy7xucm0h",
			Amount:  271_414_670_023,
		},
		// 271_414_670_023 (additional only, already claimed)
		{
			Address: "gonka1uup559m63mjcq6wyewff2su0e2wv5lsyyrcrll",
			Amount:  271_414_670_023,
		},
		// 271_021_979_397 (additional only, already claimed)
		{
			Address: "gonka1qvzmfwlcx663f6klq9050h0hs7neacy6ltyt0s",
			Amount:  271_021_979_397,
		},
		// 270_760_185_648 (additional only, already claimed)
		{
			Address: "gonka1g5t786e2hnry8c4d6fd4rh5t3vsfdnevejtsvy",
			Amount:  270_760_185_648,
		},
		// 270_629_288_773 (additional only, already claimed)
		{
			Address: "gonka1cmlns93pn4ddkn9rz4lqcedjacwg6t6vxcdzjk",
			Amount:  270_629_288_773,
		},
		// 269_778_459_087 (additional only, already claimed)
		{
			Address: "gonka1c8utgl2469d7mjlhhd4afhj3ycm6rq60y7v8dl",
			Amount:  269_778_459_087,
		},
		// 268_534_938_775 (additional only, already claimed)
		{
			Address: "gonka1gjlhk2wnct7hhya548chqjj2ezrs3za0pcxxnc",
			Amount:  268_534_938_775,
		},
		// 268_404_041_901 (additional only, already claimed)
		{
			Address: "gonka1u7l9jg4krgl8kpu9lc2ml8hscjhmyrgtv6nd2q",
			Amount:  268_404_041_901,
		},
		// 268_011_351_276 (additional only, already claimed)
		{
			Address: "gonka15vunu0new53m83ccvfcmkf84v7q4s8ldsjfu4y",
			Amount:  268_011_351_276,
		},
		// 267_160_521_589 (additional only, already claimed)
		{
			Address: "gonka1a2k2pz759kj543yzxse4hvvsyjkaw0f42mkpl2",
			Amount:  267_160_521_589,
		},
		// 267_029_624_714 (additional only, already claimed)
		{
			Address: "gonka1ujskhylfq7xqzvanuy90fm640zf459x0qtgtxy",
			Amount:  267_029_624_714,
		},
		// 266_767_830_965 (additional only, already claimed)
		{
			Address: "gonka1h2qxkeglzstqhdmuewyuwdkae0rk3l8wqxwrl5",
			Amount:  266_767_830_965,
		},
		// 266_702_382_527 (additional only, already claimed)
		{
			Address: "gonka1pks9m29wqac2kxdj8v8acq58rjshh6u8y2a772",
			Amount:  266_702_382_527,
		},
		// 266_440_588_778 (additional only, already claimed)
		{
			Address: "gonka1u9832k7znn5k4an7a9ad8nmej0jswc7ep20q3n",
			Amount:  266_440_588_778,
		},
		// 266_113_346_590 (additional only, already claimed)
		{
			Address: "gonka1xqt2sd42lzgcgvun37dcu65muyj4zfyhdhkdk6",
			Amount:  266_113_346_590,
		},
		// 127_606_979_513 + 138_233_774_343 (epoch + additional)
		{
			Address: "gonka1amlmhjym02shahjv8ldmupg4cx0qc66q6f85rj",
			Amount:  265_840_753_857,
		},
		// 265_655_207_528 (additional only, already claimed)
		{
			Address: "gonka1n8zcl8cmlf4606duwygssqq72dzlanrfsczlpv",
			Amount:  265_655_207_528,
		},
		// 265_524_310_653 (additional only, already claimed)
		{
			Address: "gonka1lajfpxh74xse4xxease0trz5gwecze99875tjf",
			Amount:  265_524_310_653,
		},
		// 265_197_068_466 (additional only, already claimed)
		{
			Address: "gonka1rtylvt6ylcdphuez6eqerj7d7pv7dc3kh6yvzc",
			Amount:  265_197_068_466,
		},
		// 264_346_238_779 (additional only, already claimed)
		{
			Address: "gonka1s5ysllxfjd9gglayqe3zpqfzsr4juq9ztzr78a",
			Amount:  264_346_238_779,
		},
		// 263_691_754_405 (additional only, already claimed)
		{
			Address: "gonka1auert95hm33pzvzau2pe2ft62krx7d39ypknxm",
			Amount:  263_691_754_405,
		},
		// 263_560_857_531 (additional only, already claimed)
		{
			Address: "gonka1qzmhx83xnamk0uqup3lxpf2te9karrwjm9cjh7",
			Amount:  263_560_857_531,
		},
		// 263_037_270_031 (additional only, already claimed)
		{
			Address: "gonka1mmd53mj5u5uepcznq3u4apyc8l7rg9g0d0ec6d",
			Amount:  263_037_270_031,
		},
		// 262_513_682_531 (additional only, already claimed)
		{
			Address: "gonka14a0856a3q78pmesxpc946xm9nsj3f275l9f5pa",
			Amount:  262_513_682_531,
		},
		// 262_448_234_094 (additional only, already claimed)
		{
			Address: "gonka1wrcr9gxu2r94chjsuk3wpcqefsv7ts0rnc94ve",
			Amount:  262_448_234_094,
		},
		// 261_335_610_658 (additional only, already claimed)
		{
			Address: "gonka1jve3qdmmea6kgcl4y5r3hhll3lm0zuz7kutqft",
			Amount:  261_335_610_658,
		},
		// 261_335_610_658 (additional only, already claimed)
		{
			Address: "gonka1c7fjlg6p7l0ynydyzkzzkxdwue7y864tytj02g",
			Amount:  261_335_610_658,
		},
		// 260_681_126_283 (additional only, already claimed)
		{
			Address: "gonka1d7sgezy57w3lqh9jkyrt3qwnqyxfmx67cn6zhc",
			Amount:  260_681_126_283,
		},
		// 260_484_780_971 (additional only, already claimed)
		{
			Address: "gonka18zgw37evwkpes7redzgh79pjy77rkv5hx4xpz7",
			Amount:  260_484_780_971,
		},
		// 259_764_848_160 (additional only, already claimed)
		{
			Address: "gonka1p57jas3hm3gmdvh64z92ycr28z968j0fn6n6jd",
			Amount:  259_764_848_160,
		},
		// 259_175_812_222 (additional only, already claimed)
		{
			Address: "gonka1pl2xyrfma445tlrjkzkc7wjmdgm5e0ala9gsfn",
			Amount:  259_175_812_222,
		},
		// 258_717_673_160 (additional only, already claimed)
		{
			Address: "gonka14zzmvt5esggym639wk0v8s3gdgdmnjzrh6p7rv",
			Amount:  258_717_673_160,
		},
		// 258_259_534_099 (additional only, already claimed)
		{
			Address: "gonka1uk73fw4tnfv9rfdeekxycvt0v8cncr64syejra",
			Amount:  258_259_534_099,
		},
		// 256_623_323_163 (additional only, already claimed)
		{
			Address: "gonka1pf4z32nqz5ax7zsxsmf4tan8wsnp957y9t4qtt",
			Amount:  256_623_323_163,
		},
		// 255_707_045_038 (additional only, already claimed)
		{
			Address: "gonka1qdulrz55xwma52j8xtmw09pgg6h5pena004s3q",
			Amount:  255_707_045_038,
		},
		// 253_678_143_478 (additional only, already claimed)
		{
			Address: "gonka1vs26xk4zvv6dv56j8r9ns7wzk6s00p8etk7wu7",
			Amount:  253_678_143_478,
		},
		// 252_434_623_168 (additional only, already claimed)
		{
			Address: "gonka1p0jj3c80gtax49285au9jqvrgqdqedyxxgz596",
			Amount:  252_434_623_168,
		},
		// 251_125_654_419 (additional only, already claimed)
		{
			Address: "gonka1yk34z38t92jzjgq2ga7tkfpj07sf4ss3tj6n2m",
			Amount:  251_125_654_419,
		},
		// 250_143_927_857 (additional only, already claimed)
		{
			Address: "gonka1gz6d9j834xvhvyt7c67ux6egugns7dlc5dzqds",
			Amount:  250_143_927_857,
		},
		// 249_489_443_483 (additional only, already claimed)
		{
			Address: "gonka1v5k8fqqfc0v798gm8svcv6tczppvegsetw4mfc",
			Amount:  249_489_443_483,
		},
		// 249_423_995_046 (additional only, already claimed)
		{
			Address: "gonka1zjajt7pt4tx0djf9r7gksur320gug5e9ymqkaf",
			Amount:  249_423_995_046,
		},
		// 249_423_995_046 (additional only, already claimed)
		{
			Address: "gonka1uv8lw89ththtj05c54wehfnqls9u2pza53q33z",
			Amount:  249_423_995_046,
		},
		// 247_984_129_422 (additional only, already claimed)
		{
			Address: "gonka1rn5nud8367cnspgas0dmtq2plxkacjsttqtdy8",
			Amount:  247_984_129_422,
		},
		// 247_591_438_798 (additional only, already claimed)
		{
			Address: "gonka13crxlqch46e2jdm6uq03g8wsxndqkpdgay6j3u",
			Amount:  247_591_438_798,
		},
		// 246_740_609_111 (additional only, already claimed)
		{
			Address: "gonka1azuz6leyh94mjyal7deprr2wpwqesr5530872r",
			Amount:  246_740_609_111,
		},
		// 242_813_702_865 (additional only, already claimed)
		{
			Address: "gonka1e2rnawlpqumhjgmkqwjkyeyhfnsgaxqdelmwqc",
			Amount:  242_813_702_865,
		},
		// 242_421_012_241 (additional only, already claimed)
		{
			Address: "gonka1du3ra0wra9l7d8uqctdrdzneufpw54k3rngjme",
			Amount:  242_421_012_241,
		},
		// 242_093_770_054 (additional only, already claimed)
		{
			Address: "gonka1lzngx5unw6qu7f97zkq6wkvmk5aze4quwn7rnt",
			Amount:  242_093_770_054,
		},
		// 236_857_895_059 (additional only, already claimed)
		{
			Address: "gonka1k445mjg7hxs55jtwvt0p0zk5qu2quqwfa50qkw",
			Amount:  236_857_895_059,
		},
		// 227_367_871_632 (additional only, already claimed)
		{
			Address: "gonka1xk4mz0avlmvzser49tfrcgsjglq4a8u05j55p6",
			Amount:  227_367_871_632,
		},
		// 215_129_013_832 (additional only, already claimed)
		{
			Address: "gonka1fczfacxeypuvc20judf2s6t92djtd8tglszmw5",
			Amount:  215_129_013_832,
		},
		// 207_733_340_403 (additional only, already claimed)
		{
			Address: "gonka1zsgtetvur6vnzlzqc75dajnqymtksmxe7ln5c0",
			Amount:  207_733_340_403,
		},
		// 202_170_223_220 (additional only, already claimed)
		{
			Address: "gonka136wz0ecwyuda2dpyngezyrdf3khk4t5zwjqqw2",
			Amount:  202_170_223_220,
		},
		// 201_777_532_596 (additional only, already claimed)
		{
			Address: "gonka1a4crzzl7wr52aydzvqjqmh44l4ygh9e2c7q2a5",
			Amount:  201_777_532_596,
		},
		// 200_534_012_286 (additional only, already claimed)
		{
			Address: "gonka16x6z79v5srx95x4xjs429dldps767n0hkd5quj",
			Amount:  200_534_012_286,
		},
		// 200_337_666_972 (additional only, already claimed)
		{
			Address: "gonka1506nmxl7xwehf5egp9qfety7yeh6rc06zrwhta",
			Amount:  200_337_666_972,
		},
		// 200_272_218_535 (additional only, already claimed)
		{
			Address: "gonka13e4hxru79npd00cp7gg6z8pqhyvela56g8pcpn",
			Amount:  200_272_218_535,
		},
		// 197_457_935_726 (additional only, already claimed)
		{
			Address: "gonka1s06n9an8mzjskm0vuejxh5vvgkdtn3t94d0lxs",
			Amount:  197_457_935_726,
		},
		// 195_625_379_478 (additional only, already claimed)
		{
			Address: "gonka10x8cx2unwzuya5m655kyuhln86q3ysy5metmqz",
			Amount:  195_625_379_478,
		},
		// 194_185_513_855 (additional only, already claimed)
		{
			Address: "gonka1jwzxe329gv3t03q339ttchpym9smv9zcgmv0za",
			Amount:  194_185_513_855,
		},
		// 190_520_401_359 (additional only, already claimed)
		{
			Address: "gonka15p7s7w2hx0y8095lddd4ummm2y0kwpwljk00aq",
			Amount:  190_520_401_359,
		},
		// 189_276_881_047 (additional only, already claimed)
		{
			Address: "gonka1jzyg4dde2ftdlg0rfg4s0kuzm9vw2gpwhzaaq5",
			Amount:  189_276_881_047,
		},
		// 188_753_293_548 (additional only, already claimed)
		{
			Address: "gonka1dq73jrtl7uw8v4qkspxw7x8ca6kv3arcz4jfa4",
			Amount:  188_753_293_548,
		},
		// 186_658_943_551 (additional only, already claimed)
		{
			Address: "gonka1ejf2q0yngtw6jmcjwe08aflkz2pgul3894a6kd",
			Amount:  186_658_943_551,
		},
		// 88_994_320_597 + 96_405_548_333 (epoch + additional)
		{
			Address: "gonka1ccx63zrqqjny476pxjdw3lslpafsgx66y7uwy5",
			Amount:  185_399_868_930,
		},
		// 185_219_077_927 (additional only, already claimed)
		{
			Address: "gonka1sgsj2eacel5g57yfwn23c99mzpz9kuq8asss9u",
			Amount:  185_219_077_927,
		},
		// 182_535_691_992 (additional only, already claimed)
		{
			Address: "gonka12ua7rs4dfx827nnqqjw93e5eu7yuq0q6fne9un",
			Amount:  182_535_691_992,
		},
		// 181_946_656_055 (additional only, already claimed)
		{
			Address: "gonka12l3fwycgvs9tdpze59za89zq4cexhkxddafxdh",
			Amount:  181_946_656_055,
		},
		// 180_703_135_744 (additional only, already claimed)
		{
			Address: "gonka1upwyydk6y4562u4u6stpxytmamxfs8trmjlvv9",
			Amount:  180_703_135_744,
		},
		// 179_983_202_933 (additional only, already claimed)
		{
			Address: "gonka108taqcfv9n0428arameap7u6f48dl2z4kvfazm",
			Amount:  179_983_202_933,
		},
		// 179_459_615_432 (additional only, already claimed)
		{
			Address: "gonka10etnufq85u67k075yuxq6h3rzwlcln5rffhlyx",
			Amount:  179_459_615_432,
		},
		// 178_739_682_621 (additional only, already claimed)
		{
			Address: "gonka1tajslmg8lwwgn6nsmtnjw6dksujcd0njgffgvy",
			Amount:  178_739_682_621,
		},
		// 176_252_641_998 (additional only, already claimed)
		{
			Address: "gonka1f8eugzqqtwfq8t00q6nyyc9hew30mc9j9n0xtv",
			Amount:  176_252_641_998,
		},
		// 173_765_601_376 (additional only, already claimed)
		{
			Address: "gonka13nm5aqjguh95v7kkte9rrq3gnn68dkduwsx33n",
			Amount:  173_765_601_376,
		},
		// 173_634_704_501 (additional only, already claimed)
		{
			Address: "gonka1pjl7nk82v5fmpah2qfjd87e97jnl6gfpm3mddj",
			Amount:  173_634_704_501,
		},
		// 171_802_148_253 (additional only, already claimed)
		{
			Address: "gonka19td66n4atq5nj2u3zy3dn39nr5jy0m934dy0vx",
			Amount:  171_802_148_253,
		},
		// 170_624_076_379 (additional only, already claimed)
		{
			Address: "gonka1j30fkfedpfqhz5zez0tkaue38hz5e06nsdekrc",
			Amount:  170_624_076_379,
		},
		// 169_773_246_693 (additional only, already claimed)
		{
			Address: "gonka19gp65p7td0e6rnh5p7cjhjf402e2rejckgy3js",
			Amount:  169_773_246_693,
		},
		// 169_380_556_069 (additional only, already claimed)
		{
			Address: "gonka1q3n37e2uc4npkheyjllx2ztx5yh42rk6vz3kgn",
			Amount:  169_380_556_069,
		},
		// 168_987_865_444 (additional only, already claimed)
		{
			Address: "gonka170wwtpvhwzgghd638z84z35gjggl7cvyzrkh8p",
			Amount:  168_987_865_444,
		},
		// 167_809_793_571 (additional only, already claimed)
		{
			Address: "gonka1ukafttpmx6m9ch3ke2v3dwfa5x5um5jtygy2mg",
			Amount:  167_809_793_571,
		},
		// 80_025_710_371 + 86_690_054_347 (epoch + additional)
		{
			Address: "gonka1af40hp4pl2rhupsss33j964a8uyrcn9j244qls",
			Amount:  166_715_764_718,
		},
		// 79_673_691_117 + 86_308_719_797 (epoch + additional)
		{
			Address: "gonka1l763nf87r604swhrwyjh4dl804ytvgkzmdrfuq",
			Amount:  165_982_410_914,
		},
		// 79_206_757_843 + 85_802_901_470 (epoch + additional)
		{
			Address: "gonka1pvfuygufq6u5qkeamedeqatmklxqy47k83xj3k",
			Amount:  165_009_659_313,
		},
		// 164_210_129_512 (additional only, already claimed)
		{
			Address: "gonka1k7e364gfhqcjngaer7hvlmj6wcezp6fejws70h",
			Amount:  164_210_129_512,
		},
		// 78_500_293_604 + 85_037_604_630 (epoch + additional)
		{
			Address: "gonka10nqlxm9jn7hswf4h8amytr6l9dgfjrz4yslhdw",
			Amount:  163_537_898_235,
		},
		// 163_162_954_513 (additional only, already claimed)
		{
			Address: "gonka12mqkaycdsc7qr37ey5rqw6vhjvkk7waxdc7rjh",
			Amount:  163_162_954_513,
		},
		// 162_770_263_888 (additional only, already claimed)
		{
			Address: "gonka179aq77jrmqmtymq0sh0qt86hkdwqsnc9uxa9ff",
			Amount:  162_770_263_888,
		},
		// 77_854_924_972 + 84_338_491_289 (epoch + additional)
		{
			Address: "gonka1wn5t79a8vl7pe90xndxe5rj4uqsntwqa2k8950",
			Amount:  162_193_416_261,
		},
		// 76_681_527_459 + 83_067_376_122 (epoch + additional)
		{
			Address: "gonka1lrnzluw7jqsyt883z2540882zczlnpa7cm3454",
			Amount:  159_748_903_582,
		},
		// 76_681_527_459 + 83_067_376_122 (epoch + additional)
		{
			Address: "gonka1mc59jg0j8exezfcv6lrgfyfrpzyehwtjvz5nju",
			Amount:  159_748_903_582,
		},
		// 75_918_819_076 + 82_241_151_264 (epoch + additional)
		{
			Address: "gonka15ckw8wekv69j0mj50e738djj924qy247dlmxhd",
			Amount:  158_159_970_341,
		},
		// 75_883_819_871 + 82_203_237_411 (epoch + additional)
		{
			Address: "gonka16crq00f4jhwvsz2m77mzldnp4uhjg0ltxzzmxv",
			Amount:  158_087_057_282,
		},
		// 154_851_002_959 (additional only, already claimed)
		{
			Address: "gonka1l9d2djn93t8hk204lnwst24zsxjhf0tm8yhee5",
			Amount:  154_851_002_959,
		},
		// 74_276_062_558 + 80_461_590_031 (epoch + additional)
		{
			Address: "gonka175m43rc7dg6xarxksu9meg9l6fp3tg2ewte6kh",
			Amount:  154_737_652_590,
		},
		// 74_158_722_807 + 80_334_478_515 (epoch + additional)
		{
			Address: "gonka1k37scwnyck5nzfqxwdq3gnw26squrtjs3ls4kr",
			Amount:  154_493_201_322,
		},
		// 73_454_684_299 + 79_571_809_415 (epoch + additional)
		{
			Address: "gonka1vtq7skdleemwn3vzh9xlt7x29a2s8ykedcc5e5",
			Amount:  153_026_493_714,
		},
		// 73_396_014_423 + 79_508_253_656 (epoch + additional)
		{
			Address: "gonka17w8dqjk6qf8jraddt5j8spcrtvxjwnn82x5cev",
			Amount:  152_904_268_080,
		},
		// 73_278_674_672 + 79_381_142_140 (epoch + additional)
		{
			Address: "gonka1l922skaf4ytgw7m9x5t08z0enrmmnxnqxuh8uc",
			Amount:  152_659_816_812,
		},
		// 152_625_756_086 (additional only, already claimed)
		{
			Address: "gonka1ag5r7em9qp7dn8nf6jecudhu38amu3nwtqv3cg",
			Amount:  152_625_756_086,
		},
		// 72_515_966_289 + 78_554_917_282 (epoch + additional)
		{
			Address: "gonka1m9u4n8rthwr0j62j26zjg4a6d39lgut45yaz7x",
			Amount:  151_070_883_571,
		},
		// 70_227_841_139 + 76_076_242_707 (epoch + additional)
		{
			Address: "gonka189qh4gle43kmcp33l6378wkswk3ll6sa832tvt",
			Amount:  146_304_083_847,
		},
		// 145_557_324_844 (additional only, already claimed)
		{
			Address: "gonka1awp2fxe98uq5s4lpxrhfms099ugxff75ks9xd6",
			Amount:  145_557_324_844,
		},
		// 145_491_876_407 (additional only, already claimed)
		{
			Address: "gonka1cajt2dwgsycyc9vwn6y74r38cs27hpnwzu222c",
			Amount:  145_491_876_407,
		},
		// 69_660_863_305 + 75_462_048_356 (epoch + additional)
		{
			Address: "gonka1gmed6tx2mr5n25xq599tfxsspxqqgdudyxrp09",
			Amount:  145_122_911_661,
		},
		// 69_419_195_089 + 75_200_254_606 (epoch + additional)
		{
			Address: "gonka1nsgfcyld8f754tl9eyr5n7yuqdezw94jfw28td",
			Amount:  144_619_449_695,
		},
		// 143_266_629_533 (additional only, already claimed)
		{
			Address: "gonka1aujpuzrahe8pvf86yr5gjd8u38wfce8x05ccat",
			Amount:  143_266_629_533,
		},
		// 67_881_046_113 + 73_534_012_375 (epoch + additional)
		{
			Address: "gonka1r4d99td5fx5lwvqv5zu5zg4rnjrsm6587lk568",
			Amount:  141_415_058_488,
		},
		// 67_646_366_611 + 73_279_789_341 (epoch + additional)
		{
			Address: "gonka1d7erkmqm6acwpugjrzytdnrcw2wgx9t4vt7jum",
			Amount:  140_926_155_953,
		},
		// 67_425_432_306 + 73_040_456_171 (epoch + additional)
		{
			Address: "gonka1227320u62rpdyw8j0ve62yv6yqzy0h67u44sqr",
			Amount:  140_465_888_477,
		},
		// 139_732_413_912 (additional only, already claimed)
		{
			Address: "gonka1vxs4azxcym36wts0t7z5nj65ma8pnkvpmywxa0",
			Amount:  139_732_413_912,
		},
		// 139_274_274_850 (additional only, already claimed)
		{
			Address: "gonka1vttxzd68fur26l9lew5netz6wxnx7hmmzdkqrn",
			Amount:  139_274_274_850,
		},
		// 66_707_648_601 + 72_262_897_208 (epoch + additional)
		{
			Address: "gonka1wxzy4uh88gxl08gwyw4l2cq6gmkjjq4yvv946r",
			Amount:  138_970_545_809,
		},
		// 138_488_893_602 (additional only, already claimed)
		{
			Address: "gonka1pp7y8xc4qshmn24k3kkj4ymxwj88aq3hkj60de",
			Amount:  138_488_893_602,
		},
		// 138_488_893_602 (additional only, already claimed)
		{
			Address: "gonka1qyw37eedekdrhu9937k2ewvh5ql99qlmsv3nj4",
			Amount:  138_488_893_602,
		},
		// 137_441_718_602 (additional only, already claimed)
		{
			Address: "gonka17ss43vkwh7x0a23cld333v8n33k7ut90j5c375",
			Amount:  137_441_718_602,
		},
		// 65_069_167_198 + 70_487_967_111 (epoch + additional)
		{
			Address: "gonka18kcyrz9hazs4p62mdusln02cnana8fxvjcgsvp",
			Amount:  135_557_134_309,
		},
		// 134_431_090_480 (additional only, already claimed)
		{
			Address: "gonka1lf8zrnapty4nny6l9dr66nd7xc4j4ktr6rujha",
			Amount:  134_431_090_480,
		},
		// 64_360_853_575 + 69_720_666_875 (epoch + additional)
		{
			Address: "gonka1jmazay9q7j62mawcu644zre75hzqnnfar00pgj",
			Amount:  134_081_520_451,
		},
		// 64_008_834_321 + 69_339_332_325 (epoch + additional)
		{
			Address: "gonka1gqruzgfhxqqg5ftvpg3945wm7mjsfxd0kq99up",
			Amount:  133_348_166_647,
		},
		// 63_422_135_565 + 68_703_774_742 (epoch + additional)
		{
			Address: "gonka1g5d7f2pwkzez2kmu92xshk56u3k957r4nyuxmn",
			Amount:  132_125_910_308,
		},
		// 61_838_048_923 + 66_987_769_268 (epoch + additional)
		{
			Address: "gonka13uwt8t3sp5ycp8lefzgm942uxfvu4svv50cl5p",
			Amount:  128_825_818_191,
		},
		// 127_624_452_988 (additional only, already claimed)
		{
			Address: "gonka18vgk40336ml0a6hc2svn0j6e7agvgu0ft7phjq",
			Amount:  127_624_452_988,
		},
		// 124_090_237_366 (additional only, already claimed)
		{
			Address: "gonka1rvjfqzm8spt5nfq84jvfdr8we8pgkv8ent08s5",
			Amount:  124_090_237_366,
		},
		// 56_731_613_741 + 61_456_082_746 (epoch + additional)
		{
			Address: "gonka1f3t8ksnaxly2w69udasl5gteejszplgg5dlhth",
			Amount:  118_187_696_487,
		},
		// 54_737_850_958 + 59_296_284_311 (epoch + additional)
		{
			Address: "gonka1jqqgjh962ndm59pk0n4j2yqqssy5gv0tgaqvd2",
			Amount:  114_034_135_269,
		},
		// 53_892_012_201 + 58_380_006_186 (epoch + additional)
		{
			Address: "gonka1vxmmas5zkp4pee0rkv9dsy54pugy7ln3fas68n",
			Amount:  112_272_018_387,
		},
		// 109_757_029_569 (additional only, already claimed)
		{
			Address: "gonka1cwgcczr7fng927htuk3nqz9m0xdaka67m3utvp",
			Amount:  109_757_029_569,
		},
		// 102_361_356_140 (additional only, already claimed)
		{
			Address: "gonka1yysfdney9ayqhgd8e03zdkhqte3pr09zzevdrq",
			Amount:  102_361_356_140,
		},
		// 101_445_078_016 (additional only, already claimed)
		{
			Address: "gonka10xfyg4v6wwvfzgf7h5fkkkt70xl2e8uy2z8ey2",
			Amount:  101_445_078_016,
		},
		// 100_070_660_829 (additional only, already claimed)
		{
			Address: "gonka1u2g98h3hwphu278djwh4zwdqpumvzyu56znz5e",
			Amount:  100_070_660_829,
		},
		// 47_463_929_391 + 51_416_608_480 (epoch + additional)
		{
			Address: "gonka1qx3znmtpxqgmz3t4tgpdk68dsfxuccn6cxnp8s",
			Amount:  98_880_537_871,
		},
		// 97_976_310_832 (additional only, already claimed)
		{
			Address: "gonka1upkmkts5wxa9ashk6hqtwvsw8x6em8stmjpu0l",
			Amount:  97_976_310_832,
		},
		// 97_845_413_957 (additional only, already claimed)
		{
			Address: "gonka1p8wl8gugrjcs46zhv087cqyr4weqxwyy6jl5z0",
			Amount:  97_845_413_957,
		},
		// 96_470_996_771 (additional only, already claimed)
		{
			Address: "gonka1f9py6qeqf7wvqcrfgykx3j0xxg63rtnkh7nxy8",
			Amount:  96_470_996_771,
		},
		// 96_405_548_333 (additional only, already claimed)
		{
			Address: "gonka1d7p03cu2y2yt3vytq9wlfm6tlz0lfhlgv9h82p",
			Amount:  96_405_548_333,
		},
		// 94_703_888_960 (additional only, already claimed)
		{
			Address: "gonka1qrna7lsqy0r9aejpvzdrn683tfx34hg5cjv6ym",
			Amount:  94_703_888_960,
		},
		// 94_572_992_085 (additional only, already claimed)
		{
			Address: "gonka17sxv5v9x57xvwt37dtza28f7z658z42czqzvny",
			Amount:  94_572_992_085,
		},
		// 93_264_023_337 (additional only, already claimed)
		{
			Address: "gonka1p2lhgng7tcqju7emk989s5fpdr7k2c3ek6h26m",
			Amount:  93_264_023_337,
		},
		// 93_198_574_899 (additional only, already claimed)
		{
			Address: "gonka1lh0danzlvxm5qtaly7myqd3n5sus0fq92n8shx",
			Amount:  93_198_574_899,
		},
		// 93_198_574_899 (additional only, already claimed)
		{
			Address: "gonka1s49nsfqljwk8er0h8ph720yw9u6u77hqsh8g79",
			Amount:  93_198_574_899,
		},
		// 92_085_951_464 (additional only, already claimed)
		{
			Address: "gonka1qk0gctucm7l3vvpxuauyw4d8pflxhxsfp868fm",
			Amount:  92_085_951_464,
		},
		// 43_439_861_853 + 47_057_426_512 (epoch + additional)
		{
			Address: "gonka1xyxak0gyx76hrrkwukvyc6g2rpkruwjkpuh0p6",
			Amount:  90_497_288_365,
		},
		// 88_617_184_280 (additional only, already claimed)
		{
			Address: "gonka1h9asnq9x2kux2m8pcwaeegljzmd4ym4h26p07n",
			Amount:  88_617_184_280,
		},
		// 87_897_251_467 (additional only, already claimed)
		{
			Address: "gonka1qre9aepalndahwj37xrcepndmut2vu9dr3kh4t",
			Amount:  87_897_251_467,
		},
		// 87_570_009_280 (additional only, already claimed)
		{
			Address: "gonka19k7cswucfu8mz9fszkdnyp66lvjddaa55q3hdw",
			Amount:  87_570_009_280,
		},
		// 86_130_143_657 (additional only, already claimed)
		{
			Address: "gonka1xdx6tywcwjj74hmyxdr7v9mexg58zp7scuwumn",
			Amount:  86_130_143_657,
		},
		// 40_902_345_583 + 44_308_592_139 (epoch + additional)
		{
			Address: "gonka13hqh2wcv3jaeqq5yfxqq5fce66tp2vyeu582jc",
			Amount:  85_210_937_722,
		},
		// 84_624_829_596 (additional only, already claimed)
		{
			Address: "gonka1jyqp04yrjn8vdwqwkuwfd8nqal4d4vr3lpdyuk",
			Amount:  84_624_829_596,
		},
		// 83_839_448_347 (additional only, already claimed)
		{
			Address: "gonka109kcrv69kse73734uahg2jpht2dr9xdhpfgacm",
			Amount:  83_839_448_347,
		},
		// 81_156_062_412 (additional only, already claimed)
		{
			Address: "gonka1z3kelh3gx3t6kz303f8trdll3j5ap3zz8csyfm",
			Amount:  81_156_062_412,
		},
		// 38_898_127_548 + 42_137_467_765 (epoch + additional)
		{
			Address: "gonka1cgsj4fald9ppasaxj3le0q5nlpzekg0p70yc86",
			Amount:  81_035_595_313,
		},
		// 80_763_371_788 (additional only, already claimed)
		{
			Address: "gonka1r7s7ael2cz3ftshsr25v4syk0gns960nxjq5e2",
			Amount:  80_763_371_788,
		},
		// 80_436_129_601 (additional only, already claimed)
		{
			Address: "gonka182s2d5pre7k3q2qacp06fdr6j34czwyahg8ed7",
			Amount:  80_436_129_601,
		},
		// 79_847_093_664 (additional only, already claimed)
		{
			Address: "gonka13lduw4kdvyyf05psepnncm306k4wuyfwcg7y78",
			Amount:  79_847_093_664,
		},
		// 37_821_075_827 + 40_970_721_830 (epoch + additional)
		{
			Address: "gonka1zrzapmfar303hfdqpfglkcxn5xf0s595zqt2c8",
			Amount:  78_791_797_657,
		},
		// 37_639_824_665 + 40_774_376_518 (epoch + additional)
		{
			Address: "gonka1uuf3jr427y6hhx00nuk4g930vmyvjeesp84u6z",
			Amount:  78_414_201_183,
		},
		// 75_069_357_732 (additional only, already claimed)
		{
			Address: "gonka180h3x4dv834pyn2ua3k596hrsk7d2l5utljtg6",
			Amount:  75_069_357_732,
		},
		// 75_003_909_294 (additional only, already claimed)
		{
			Address: "gonka19am2quuhpyqh5fp22qhytdre50pmnjfsl3927h",
			Amount:  75_003_909_294,
		},
		// 74_873_012_419 (additional only, already claimed)
		{
			Address: "gonka1ta6mqt9guj3rvg874nker75wkac2jszs4js46k",
			Amount:  74_873_012_419,
		},
		// 73_956_734_295 (additional only, already claimed)
		{
			Address: "gonka1qy72uqasgd00un7qwexvfdl4zzy8ejaqj2zkqc",
			Amount:  73_956_734_295,
		},
		// 71_207_899_923 (additional only, already claimed)
		{
			Address: "gonka1h49wte7d29sag08gg63lcyn4t9a49dk4m03mnn",
			Amount:  71_207_899_923,
		},
		// 71_207_899_923 (additional only, already claimed)
		{
			Address: "gonka1sqsj2qdw0wxgwuv25zhlffz5fufe4fnp0wuqt8",
			Amount:  71_207_899_923,
		},
		// 69_833_482_737 (additional only, already claimed)
		{
			Address: "gonka1j72x6qpf0aetl8pyvzx5xlgp2dmp60447t4uxl",
			Amount:  69_833_482_737,
		},
		// 33_265_819_487 + 36_036_114_966 (epoch + additional)
		{
			Address: "gonka1m8czp8f4z2qgtrfyzm3sa795l2vsxy0pqa47sj",
			Amount:  69_301_934_453,
		},
		// 68_655_410_863 (additional only, already claimed)
		{
			Address: "gonka1hjjse9r0zfe0q8k4hfe4t5xuzstjqvky7dnj05",
			Amount:  68_655_410_863,
		},
		// 68_524_513_988 (additional only, already claimed)
		{
			Address: "gonka1p5882nxdv07qaxr0d75pwzg2qwft39qsrc0q0g",
			Amount:  68_524_513_988,
		},
		// 31_960_621_586 + 34_622_223_400 (epoch + additional)
		{
			Address: "gonka1w994snv5susy327tnjlxr86q6udc3x8pe8q4tg",
			Amount:  66_582_844_986,
		},
		// 66_561_060_866 (additional only, already claimed)
		{
			Address: "gonka19ghzvgfr065s3fr5awuvs3nhy9fq4n7wrr9kel",
			Amount:  66_561_060_866,
		},
		// 66_168_370_241 (additional only, already claimed)
		{
			Address: "gonka1avvxhsspwlj6c0w3p5xwqdvlpl26dc5s0c8pmf",
			Amount:  66_168_370_241,
		},
		// 65_448_437_429 (additional only, already claimed)
		{
			Address: "gonka1598sglu00mktw3973jmwsu3vdw29mgcfjleju2",
			Amount:  65_448_437_429,
		},
		// 64_924_849_930 (additional only, already claimed)
		{
			Address: "gonka17gxetlx3d90zfzpflldnf2n4wyj0naerlze0dx",
			Amount:  64_924_849_930,
		},
		// 61_325_185_871 (additional only, already claimed)
		{
			Address: "gonka1jyc3hel96ml8txk2yvj0hmnwdgrvwrckvvf2gd",
			Amount:  61_325_185_871,
		},
		// 57_790_970_250 (additional only, already claimed)
		{
			Address: "gonka17exwulhw83ehh6lzkkj2nfdusjwlls2m3wsfdw",
			Amount:  57_790_970_250,
		},
		// 57_201_934_313 (additional only, already claimed)
		{
			Address: "gonka1crrknngvl6w2nmff2gjx0fha339432dhgf27kd",
			Amount:  57_201_934_313,
		},
		// 27_368_925_479 + 29_648_142_155 (epoch + additional)
		{
			Address: "gonka1lan2x2er0djczj7rumtdn29rnr5g870cn0tfk0",
			Amount:  57_017_067_634,
		},
		// 56_416_553_064 (additional only, already claimed)
		{
			Address: "gonka1lf3jye2fvsvkjtaqj0v0fk36ejumg5f57q8ahj",
			Amount:  56_416_553_064,
		},
		// 26_100_167_344 + 28_273_724_969 (epoch + additional)
		{
			Address: "gonka1ye7unfm0nf6qa7awmcgxql7u2zq9fqv0g5qw3h",
			Amount:  54_373_892_313,
		},
		// 25_435_579_749 + 27_553_792_157 (epoch + additional)
		{
			Address: "gonka1skf6lv8rwvugww5tap9sfuhxzkw27g6zsz8wy6",
			Amount:  52_989_371_906,
		},
		// 25_314_745_641 + 27_422_895_282 (epoch + additional)
		{
			Address: "gonka1uf85kdk97p45cazueu0jmyxyd5h63c3glwnnug",
			Amount:  52_737_640_923,
		},
		// 52_227_853_069 (additional only, already claimed)
		{
			Address: "gonka1au034ld8fsrvtyxpzxx8nnnqe5kmhe20lmhuwm",
			Amount:  52_227_853_069,
		},
		// 24_710_575_101 + 26_768_410_908 (epoch + additional)
		{
			Address: "gonka1t4xds4ltl85hyj025aksrvpuqrwfgmvxv9nwz5",
			Amount:  51_478_986_009,
		},
		// 50_853_435_882 (additional only, already claimed)
		{
			Address: "gonka1ptc29e7kfydvx6093qtlsh9vt2rndlw5zdt3sp",
			Amount:  50_853_435_882,
		},
		// 24_348_072_777 + 26_375_720_284 (epoch + additional)
		{
			Address: "gonka1hkxve0xr274qfsyjzdgezh5j89teya73lwnwvx",
			Amount:  50_723_793_061,
		},
		// 23_985_570_453 + 25_983_029_659 (epoch + additional)
		{
			Address: "gonka109xyam3aq9xt6tjgkgtwwahky7hfd84nl8tvup",
			Amount:  49_968_600_112,
		},
		// 49_806_260_883 (additional only, already claimed)
		{
			Address: "gonka13x3udp8tx4yejz269h87sq27jn8jm6m5uzh5vx",
			Amount:  49_806_260_883,
		},
		// 23_562_651_074 + 25_524_890_596 (epoch + additional)
		{
			Address: "gonka18tzk9e0wxed2qx9m0uefr9qpv7pqlju9rs7tlc",
			Amount:  49_087_541_670,
		},
		// 48_955_431_197 (additional only, already claimed)
		{
			Address: "gonka10r7l6unulyg0wtk8kl980k7fpvjskxuxdtnt7t",
			Amount:  48_955_431_197,
		},
		// 48_170_049_947 (additional only, already claimed)
		{
			Address: "gonka1f4mcr5pzn9rtdyzdv9dfg4x47vrqxk0f2ujqs7",
			Amount:  48_170_049_947,
		},
		// 45_552_112_451 (additional only, already claimed)
		{
			Address: "gonka1mxn2p7c0y47syy58dqdmwwr66q77xgqmfdqhvq",
			Amount:  45_552_112_451,
		},
		// 21_750_139_453 + 23_561_437_474 (epoch + additional)
		{
			Address: "gonka16vz2gpallzd2e8gcv3834rr0x4xnqrp2cfresm",
			Amount:  45_311_576_927,
		},
		// 45_290_318_701 (additional only, already claimed)
		{
			Address: "gonka15llevgky75a7jknr5m0kzme669xuwrqdxex5f3",
			Amount:  45_290_318_701,
		},
		// 21_179_825_105 + 22_943_628_753 (epoch + additional)
		{
			Address: "gonka1exdrn4u567mv9qcauycd3s4ezavxpdp8ucu8re",
			Amount:  44_123_453_858,
		},
		// 42_934_174_953 (additional only, already claimed)
		{
			Address: "gonka1c7nulmsg8enww58gx4tazzmqe0jk5he39tme7g",
			Amount:  42_934_174_953,
		},
		// 20_602_215_426 + 22_317_917_162 (epoch + additional)
		{
			Address: "gonka19tvnzlndce56yjskv9ggxtrf0dtsld6yj2hhtj",
			Amount:  42_920_132_588,
		},
		// 40_774_376_518 (additional only, already claimed)
		{
			Address: "gonka1m8z74q6y6hcykh4t9s83l0qyt47lhdtx0wp4cy",
			Amount:  40_774_376_518,
		},
		// 40_381_685_894 (additional only, already claimed)
		{
			Address: "gonka1e20pejla34fwaj7gp4vr0xuxxde9hzrcwj7y2t",
			Amount:  40_381_685_894,
		},
		// 40_316_237_456 (additional only, already claimed)
		{
			Address: "gonka1kx9mca3xm8u8ypzfuhmxey66u0ufxhs7nm6wc5",
			Amount:  40_316_237_456,
		},
		// 40_316_237_456 (additional only, already claimed)
		{
			Address: "gonka1pvmxfx2cy6s2d57tp98mklkkv83xnjjn6y96r4",
			Amount:  40_316_237_456,
		},
		// 39_988_995_269 (additional only, already claimed)
		{
			Address: "gonka15hc8eqp4axp7lztjuk0y5z0af6drw5k0eltrue",
			Amount:  39_988_995_269,
		},
		// 39_923_546_832 (additional only, already claimed)
		{
			Address: "gonka1ujnc662v6g69jm6fgxnr79a2m7ehzeut059239",
			Amount:  39_923_546_832,
		},
		// 39_727_201_519 (additional only, already claimed)
		{
			Address: "gonka1hnye4t9y9g6t2vg2m9wy3jj7cgcamteu27lnjy",
			Amount:  39_727_201_519,
		},
		// 38_680_026_520 (additional only, already claimed)
		{
			Address: "gonka1yv5nq3kzyaa0urcdfs4cu0ampcdw9d2c5dk0j4",
			Amount:  38_680_026_520,
		},
		// 37_567_403_084 (additional only, already claimed)
		{
			Address: "gonka155g6nkjql5sgaz0j8ugr9ghc62heflj9g84mqz",
			Amount:  37_567_403_084,
		},
		// 37_501_954_646 (additional only, already claimed)
		{
			Address: "gonka1cnkw5c7d68rym3yr9yqtsesmjqzxss2ax4rwlt",
			Amount:  37_501_954_646,
		},
		// 36_782_021_835 (additional only, already claimed)
		{
			Address: "gonka1l7kftz35d67xnzppm3esrrrpumhwajhg7lr5cz",
			Amount:  36_782_021_835,
		},
		// 17_581_362_725 + 19_045_495_292 (epoch + additional)
		{
			Address: "gonka1ycyya4sw9qyfhzq5c8mz9xqgdyd5jcxaaw8343",
			Amount:  36_626_858_017,
		},
		// 36_323_882_773 (additional only, already claimed)
		{
			Address: "gonka1nauzjd0ke0tw64zcn5xdc5jla95evspd0ezzc6",
			Amount:  36_323_882_773,
		},
		// 36_062_089_023 (additional only, already claimed)
		{
			Address: "gonka1y2a9p56kv044327uycmqdexl7zs82fs5ryv5le",
			Amount:  36_062_089_023,
		},
		// 34_818_568_712 (additional only, already claimed)
		{
			Address: "gonka1xzajvz25mj3jaujmdu3c07phzwdm3zy0xh4yx9",
			Amount:  34_818_568_712,
		},
		// 34_425_878_087 (additional only, already claimed)
		{
			Address: "gonka1zv89w02sdfzwy74s9vcthk38dnfsw36s4h7s93",
			Amount:  34_425_878_087,
		},
		// 33_836_842_150 (additional only, already claimed)
		{
			Address: "gonka1dkl4mah5erqggvhqkpc8j3qs5tyuetgdy552cp",
			Amount:  33_836_842_150,
		},
		// 16_010_519_320 + 17_343_835_918 (epoch + additional)
		{
			Address: "gonka14d939yheufy96w9wjyp5a36qyzg333v0nja4gj",
			Amount:  33_354_355_238,
		},
		// 15_829_268_157 + 17_147_490_605 (epoch + additional)
		{
			Address: "gonka10rzq9m43l6dl2zz9c60man27gnqmw3q84rfahq",
			Amount:  32_976_758_762,
		},
		// 15_345_931_725 + 16_623_903_106 (epoch + additional)
		{
			Address: "gonka14trttth9yraycnhhmtz2anjctnu547pd8wm9pj",
			Amount:  31_969_834_831,
		},
		// 15_345_931_725 + 16_623_903_106 (epoch + additional)
		{
			Address: "gonka1fl2ux2967t2pgcqf9evta9lc4l3a4l2gale7tg",
			Amount:  31_969_834_831,
		},
		// 31_807_940_590 (additional only, already claimed)
		{
			Address: "gonka1gd79gdm2s35rc4mhssgwrqh2lnmaff0wq4mwq6",
			Amount:  31_807_940_590,
		},
		// 31_349_801_528 (additional only, already claimed)
		{
			Address: "gonka13jw6k5lleqepjyjsm8r4lhrfnn955h8wrexdz2",
			Amount:  31_349_801_528,
		},
		// 31_218_904_654 (additional only, already claimed)
		{
			Address: "gonka1ey9dfu98ngcjpg3lkmwc69xdsp2d9eaknyyl28",
			Amount:  31_218_904_654,
		},
		// 14_843_478_536 + 16_079_606_854 (epoch + additional)
		{
			Address: "gonka10dn20l38dxx4a7rfnd9qwdass86j27c79w3tm7",
			Amount:  30_923_085_391,
		},
		// 30_891_662_466 (additional only, already claimed)
		{
			Address: "gonka1ndlfjnp5d7eruyrhjvn6uape3jp630qld7l9yl",
			Amount:  30_891_662_466,
		},
		// 30_826_214_028 (additional only, already claimed)
		{
			Address: "gonka1a59eyhlm8cla6f0mcw8nagasg36nu0dnznh9l7",
			Amount:  30_826_214_028,
		},
		// 30_433_523_404 (additional only, already claimed)
		{
			Address: "gonka13fcjk6av5s3hll54eqlyn80u8l0crr8mmp6h0s",
			Amount:  30_433_523_404,
		},
		// 30_171_729_654 (additional only, already claimed)
		{
			Address: "gonka1pzdzdgd3auppy7x8a73suxaucgjcutfevrwzjp",
			Amount:  30_171_729_654,
		},
		// 30_171_729_654 (additional only, already claimed)
		{
			Address: "gonka17et3ctw6t4d5ylnuc2kwc8xrlwk69y0cggzjqm",
			Amount:  30_171_729_654,
		},
		// 29_779_039_030 (additional only, already claimed)
		{
			Address: "gonka15nnxl4ywpj58zq34svvent9y2gv3g92dsf0n2g",
			Amount:  29_779_039_030,
		},
		// 29_582_693_718 (additional only, already claimed)
		{
			Address: "gonka185kgdje9wjucxtkdu0nqhpqlwxutc5kc7nss8u",
			Amount:  29_582_693_718,
		},
		// 29_582_693_718 (additional only, already claimed)
		{
			Address: "gonka14p0njxtlnx2c8zn570nvvaf730gnnkwvetlwj8",
			Amount:  29_582_693_718,
		},
		// 14_077_173_590 + 15_249_485_920 (epoch + additional)
		{
			Address: "gonka1w6cjf08fgm3yalqf7ytt0hz6thlhs53d2fn7j2",
			Amount:  29_326_659_510,
		},
		// 29_124_554_655 (additional only, already claimed)
		{
			Address: "gonka1ct24nkkykeyxwhd0nr9tryhhpaak7zggrf2axc",
			Amount:  29_124_554_655,
		},
		// 29_059_106_218 (additional only, already claimed)
		{
			Address: "gonka1dydrqvyaa0s8puwmk9e5d5x5hchrm883eckngj",
			Amount:  29_059_106_218,
		},
		// 13_895_922_428 + 15_053_140_608 (epoch + additional)
		{
			Address: "gonka1md534cgw67whyacx43e4z805frz40m3pjlrkyf",
			Amount:  28_949_063_036,
		},
		// 28_928_209_343 (additional only, already claimed)
		{
			Address: "gonka1rccd6tq06vl2tq2rz9p6uxuuc8tn9878le92gp",
			Amount:  28_928_209_343,
		},
		// 28_731_864_031 (additional only, already claimed)
		{
			Address: "gonka1fxdt48vp78uxa7apuuamv4clwafxagnjg9eulc",
			Amount:  28_731_864_031,
		},
		// 28_666_415_593 (additional only, already claimed)
		{
			Address: "gonka16v0ut8km4ccfzr2ste4zeegzpwrvx70z7kpzyn",
			Amount:  28_666_415_593,
		},
		// 28_666_415_593 (additional only, already claimed)
		{
			Address: "gonka1myqk82deh6wexqazulwaqm6x340vh8kch44qwl",
			Amount:  28_666_415_593,
		},
		// 28_600_967_156 (additional only, already claimed)
		{
			Address: "gonka1yuhp76axp2uvnflmlhjch2cmqcp0xf7595zklc",
			Amount:  28_600_967_156,
		},
		// 28_142_828_094 (additional only, already claimed)
		{
			Address: "gonka1qlt90c4hxccgnuc478jaa3xl5klga84xzrvpda",
			Amount:  28_142_828_094,
		},
		// 28_077_379_657 (additional only, already claimed)
		{
			Address: "gonka17gffv7lusn36e3sqxymn94khnq709py24ufxfz",
			Amount:  28_077_379_657,
		},
		// 27_946_482_782 (additional only, already claimed)
		{
			Address: "gonka199lgrq8l9xcqqnr0agajzl78c4dpfvwnsc4elm",
			Amount:  27_946_482_782,
		},
		// 27_946_482_782 (additional only, already claimed)
		{
			Address: "gonka1a32tfg0a3xe7zer9m3ttuxr57wdffpc82qnacq",
			Amount:  27_946_482_782,
		},
		// 27_881_034_345 (additional only, already claimed)
		{
			Address: "gonka1cz4d6jc2lrwj8tg69y5gunl39wr8era9l4qta4",
			Amount:  27_881_034_345,
		},
		// 27_684_689_032 (additional only, already claimed)
		{
			Address: "gonka1p0pu0d98mseulw5sjcueacl4m8ecrgqethmvrc",
			Amount:  27_684_689_032,
		},
		// 13_170_917_780 + 14_267_759_359 (epoch + additional)
		{
			Address: "gonka1guzkdc9hp24shxslgsgdhde4p2k039ealazwqs",
			Amount:  27_438_677_139,
		},
		// 27_422_895_282 (additional only, already claimed)
		{
			Address: "gonka15k9c2xzuman9mtggecwed2kpts0y7wgnnrgzvu",
			Amount:  27_422_895_282,
		},
		// 26_702_962_471 (additional only, already claimed)
		{
			Address: "gonka10cllk38hyhjgz96dv2x586wd4thntwx8n7yw54",
			Amount:  26_702_962_471,
		},
		// 12_808_415_456 + 13_875_068_735 (epoch + additional)
		{
			Address: "gonka100mrlhdj7rvj9dxskmtkerhcrmpjq7lx95z8hy",
			Amount:  26_683_484_191,
		},
		// 26_441_168_721 (additional only, already claimed)
		{
			Address: "gonka1tml58t8c87c65amrqee5rpurmlskr9v9p3de62",
			Amount:  26_441_168_721,
		},
		// 26_375_720_284 (additional only, already claimed)
		{
			Address: "gonka1hrvp8gaxd9y43uuphgpmgmp3xlau3veeq9x6xj",
			Amount:  26_375_720_284,
		},
		// 26_310_271_846 (additional only, already claimed)
		{
			Address: "gonka1wnhd4uqvf3k38av7m9s3ssdzu0kvqskmes69gu",
			Amount:  26_310_271_846,
		},
		// 26_048_478_097 (additional only, already claimed)
		{
			Address: "gonka14qdqv958j8tmc62jddyj55pl0vhqr8uauac7j5",
			Amount:  26_048_478_097,
		},
		// 25_983_029_659 (additional only, already claimed)
		{
			Address: "gonka1pjsg254jtuaz4rm9kq27nyplrp266x79mxffqw",
			Amount:  25_983_029_659,
		},
		// 25_852_132_783 (additional only, already claimed)
		{
			Address: "gonka1vlcd8tr2nh5x6h4wl9vacpzucpp9a2p9rv8c57",
			Amount:  25_852_132_783,
		},
		// 12_204_244_915 + 13_220_584_360 (epoch + additional)
		{
			Address: "gonka1ddswmmmn38esxegjf6qw36mt4aqyw6etvysy5x",
			Amount:  25_424_829_275,
		},
		// 25_328_545_284 (additional only, already claimed)
		{
			Address: "gonka1c80ja2yjn6jfgv7e7hag9mgq3s538lzwajk4e0",
			Amount:  25_328_545_284,
		},
		// 25_197_648_410 (additional only, already claimed)
		{
			Address: "gonka1desd6924c4aturcdmwk59jpye5em3q2ze8gvjn",
			Amount:  25_197_648_410,
		},
		// 25_132_199_972 (additional only, already claimed)
		{
			Address: "gonka1zqw2tpr69zj44qv38flanuyce9ufakpdvr4xnv",
			Amount:  25_132_199_972,
		},
		// 25_066_751_535 (additional only, already claimed)
		{
			Address: "gonka1yjcp4wm5ue2vsxf48wk7203a38lnetm4xfaqau",
			Amount:  25_066_751_535,
		},
		// 25_066_751_535 (additional only, already claimed)
		{
			Address: "gonka19f3n6z7pdz7q8kp7vgyvx5jkm47h3lxzqz2rj9",
			Amount:  25_066_751_535,
		},
		// 11_781_325_537 + 12_762_445_298 (epoch + additional)
		{
			Address: "gonka1jt5mqrx0yg5k6qdqhnkc4kpya4mzy52ngyh34c",
			Amount:  24_543_770_835,
		},
		// 23_430_540_599 (additional only, already claimed)
		{
			Address: "gonka1q96dv6st48490vtzx2pwuwkjg439dr4hg6dzjn",
			Amount:  23_430_540_599,
		},
		// 22_906_953_100 (additional only, already claimed)
		{
			Address: "gonka184jz8ja5gqkr3kt98a3a4syyypwj07j0qngukg",
			Amount:  22_906_953_100,
		},
		// 22_448_814_037 (additional only, already claimed)
		{
			Address: "gonka1wyhnm2w3hpah5kfyjc0jnha4mcdfwuukr36k96",
			Amount:  22_448_814_037,
		},
		// 22_448_814_037 (additional only, already claimed)
		{
			Address: "gonka1vp9m8e9yw6yky45fkcekspx9u3useaj0aa37qn",
			Amount:  22_448_814_037,
		},
		// 22_121_571_850 (additional only, already claimed)
		{
			Address: "gonka1xzt80teg2sx6245gt3p96qax6dcdr56f6keqwr",
			Amount:  22_121_571_850,
		},
		// 21_467_087_476 (additional only, already claimed)
		{
			Address: "gonka1gjq6euu48kuvfcpm29a4ndsh4fv5cuplv2w54u",
			Amount:  21_467_087_476,
		},
		// 20_485_360_914 (additional only, already claimed)
		{
			Address: "gonka12u5pjkvehkhzky5s0yn3j0teseqa2wk3m26aq5",
			Amount:  20_485_360_914,
		},
		// 9_666_728_646 + 10_471_749_988 (epoch + additional)
		{
			Address: "gonka14cqs3cnsahrr52end3twr7sq3rhtwnj8j8pyzv",
			Amount:  20_138_478_634,
		},
		// 9_062_558_105 + 9_817_265_613 (epoch + additional)
		{
			Address: "gonka1sy673yew46jqh7c7h77qt0sq32ryjwrkmy66tf",
			Amount:  18_879_823_718,
		},
		// 18_718_253_104 (additional only, already claimed)
		{
			Address: "gonka1n8fxs9k92q7z9r2mffcpwwejll583mkc4em2rj",
			Amount:  18_718_253_104,
		},
		// 18_129_217_167 (additional only, already claimed)
		{
			Address: "gonka1w2pxkv4v6r5h6sdvges287l2g5rsd3r4x77080",
			Amount:  18_129_217_167,
		},
		// 8_579_221_673 + 9_293_678_114 (epoch + additional)
		{
			Address: "gonka1xaqsz52keh4td77z5g792kq5f2yqkukdvzmvyf",
			Amount:  17_872_899_787,
		},
		// 8_458_387_565 + 9_162_781_239 (epoch + additional)
		{
			Address: "gonka1kre73lv6ylwaxvfmr3aegesnjvxufj9hctyslr",
			Amount:  17_621_168_804,
		},
		// 8_277_136_403 + 8_966_435_927 (epoch + additional)
		{
			Address: "gonka1dgkjlfqrm56tr6evkydg2qkkldzancnm097hj4",
			Amount:  17_243_572_330,
		},
		// 17_082_042_168 (additional only, already claimed)
		{
			Address: "gonka19h26fcdqq54uv6mv2n6vtr23avhsfz5ehx0pu9",
			Amount:  17_082_042_168,
		},
		// 8_095_885_241 + 8_770_090_615 (epoch + additional)
		{
			Address: "gonka16lksjaqq2gqpeexvqg4xgux6hdtms3wnk4jjle",
			Amount:  16_865_975_856,
		},
		// 16_296_660_919 (additional only, already claimed)
		{
			Address: "gonka1adzm2h68q36j0lzcgl8cnhe7qse0jnlfdnl8k5",
			Amount:  16_296_660_919,
		},
		// 16_231_212_482 (additional only, already claimed)
		{
			Address: "gonka1vkh8e2uqk9mdw0ql83g0p4ge0wmqstcasulngn",
			Amount:  16_231_212_482,
		},
		// 15_903_970_295 (additional only, already claimed)
		{
			Address: "gonka1rc6f9ejjzjkmvhs7seztw0208yj206llspmcwf",
			Amount:  15_903_970_295,
		},
		// 7_129_212_376 + 7_722_915_616 (epoch + additional)
		{
			Address: "gonka10cgs0yszeu9r9yh8q3a4xqumr2xrllpnzw04v0",
			Amount:  14_852_127_992,
		},
		// 13_678_723_421 (additional only, already claimed)
		{
			Address: "gonka1rmr4x8lyw76gzuyzta68gj965zfa00gzh2aszz",
			Amount:  13_678_723_421,
		},
		// 12_500_651_548 (additional only, already claimed)
		{
			Address: "gonka1kjrvy4xme4ctepdjspjdw677nmvdwlps4hl5h8",
			Amount:  12_500_651_548,
		},
		// 12_369_754_674 (additional only, already claimed)
		{
			Address: "gonka1hmjjq6ghykww8f8ujajnnmu2dpk2drrth8mtva",
			Amount:  12_369_754_674,
		},
		// 12_042_512_486 (additional only, already claimed)
		{
			Address: "gonka18v2az8fn2z28ykxu7zyc09crqdmjuldmnag0g2",
			Amount:  12_042_512_486,
		},
		// 11_911_615_611 (additional only, already claimed)
		{
			Address: "gonka1m7df6745x0vr02pf2lr369vapfzddh3jxhawvq",
			Amount:  11_911_615_611,
		},
		// 5_618_786_025 + 6_086_704_680 (epoch + additional)
		{
			Address: "gonka1tgtsqayddf96dwxry4mnu5qy0ae377q8few3cp",
			Amount:  11_705_490_705,
		},
		// 11_584_373_424 (additional only, already claimed)
		{
			Address: "gonka1zeaucp7k6cauj84e92wccnxnsua0a73rl8tpvj",
			Amount:  11_584_373_424,
		},
		// 4_893_781_377 + 5_301_323_431 (epoch + additional)
		{
			Address: "gonka1pfgtn4aa7hy5jwxe5wfll8n4d5dmqgughpdu6s",
			Amount:  10_195_104_808,
		},
		// 4_652_113_160 + 5_039_529_681 (epoch + additional)
		{
			Address: "gonka1mmwngrtpc4fd4jquv5vfsnscqdzmcz0zgrvy0g",
			Amount:  9_691_642_841,
		},
		// 4_591_696_106 + 4_974_081_243 (epoch + additional)
		{
			Address: "gonka1f7d5j263cw6ukux38kg5gglup83aazctchmtp8",
			Amount:  9_565_777_349,
		},
		// 8_704_642_178 (additional only, already claimed)
		{
			Address: "gonka1hn9wmlfe7nha0x26p7nsqvk9g8pnwml74hrm8n",
			Amount:  8_704_642_178,
		},
		// 3_866_691_458 + 4_188_699_995 (epoch + additional)
		{
			Address: "gonka1w6439pku8l32dzxrvy73y6t85jf8azfet7ulnf",
			Amount:  8_055_391_453,
		},
		// 7_657_467_178 (additional only, already claimed)
		{
			Address: "gonka15fpzwc7d2z3r4y5475p6k7uqknnkj38gsus3d9",
			Amount:  7_657_467_178,
		},
		// 6_741_189_054 (additional only, already claimed)
		{
			Address: "gonka16tlht0m9aanxklqddt3xlwwxr5axw9yydg52wu",
			Amount:  6_741_189_054,
		},
		// 5_824_910_930 (additional only, already claimed)
		{
			Address: "gonka1948n747ayualpznexxchnx95pdnpu25mph804e",
			Amount:  5_824_910_930,
		},
		// 4_385_045_307 (additional only, already claimed)
		{
			Address: "gonka16dhzkyjqvqt2f6h9s9f0fx0dpqaukq5hu47uul",
			Amount:  4_385_045_307,
		},
		// 4_057_803_120 (additional only, already claimed)
		{
			Address: "gonka1ra2jy33a2uuyu4jq2f9gf8va555f4tkhr3e4j9",
			Amount:  4_057_803_120,
		},
		// 3_730_560_933 (additional only, already claimed)
		{
			Address: "gonka1yps5566s8222gefuwxep5yusv5fndvact0v2jq",
			Amount:  3_730_560_933,
		},
		// 3_206_973_433 (additional only, already claimed)
		{
			Address: "gonka1gkdl40hfau92xwh39updqzzrpllaqv7fjanhlm",
			Amount:  3_206_973_433,
		},
		// 3_010_628_120 (additional only, already claimed)
		{
			Address: "gonka1yq4vwn7fc9x7lykjhc0x3e7r2atee32czy34mt",
			Amount:  3_010_628_120,
		},
		// 2_552_489_059 (additional only, already claimed)
		{
			Address: "gonka1t88kkvnrayz5l2u043tccd2pks4rt8y346z9dv",
			Amount:  2_552_489_059,
		},
		// 785_381_248 (additional only, already claimed)
		{
			Address: "gonka1qn23ufgpfs6vyg0jkx3pqpq2asp3m950wcexn0",
			Amount:  785_381_248,
		},
	}

	// Bounty Program
	bountyProgramRewards = []BountyReward{
		// Bug Bounty: Non Determinism in denom
		{
			Address: "gonka1z0phwa33ggyzs4djmntsahcjjrjqjyyjsg8f86",
			Amount:  12_000_000_000,
		},
		// Bug Bounty: CICD Vulnerability
		{
			Address: "gonka1z0phwa33ggyzs4djmntsahcjjrjqjyyjsg8f86",
			Amount:  2_500_000_000,
		},
		// Bug Bounty: Low-VRAM GPUs
		{
			Address: "gonka1wpan224906ant68frjd8vqreaxr87hudy2wvd9",
			Amount:  3_500_000_000,
		},
	}
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	distrKeeper distrkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := setV0_2_7Params(ctx, k); err != nil {
			return nil, err
		}

		if err := distributeBountyRewards(ctx, k, distrKeeper); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func setV0_2_7Params(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParamsSafe(ctx)
	if err != nil {
		return err
	}

	// Genesis guardian maturity gating + guardian address migration (genesis-only -> governance params).
	//
	// - threshold: 20,000,000
	// - min height: 3,000,000
	// - guardian addresses: copied from legacy GenesisOnlyParams if present
	if params.GenesisGuardianParams == nil {
		params.GenesisGuardianParams = &types.GenesisGuardianParams{}
	}
	params.GenesisGuardianParams.NetworkMaturityThreshold = 15_000_000
	params.GenesisGuardianParams.NetworkMaturityMinHeight = 3_000_000

	if legacy, found := k.GetGenesisOnlyParams(ctx); found {
		// Only overwrite if legacy has something (avoid wiping if already set by governance earlier).
		if len(legacy.GenesisGuardianAddresses) > 0 && len(params.GenesisGuardianParams.GuardianAddresses) == 0 {
			params.GenesisGuardianParams.GuardianAddresses = legacy.GenesisGuardianAddresses
		}
	}

	// Developer access gating: restrict inference requests to a developer allowlist until a fixed cutoff height.
	// NOTE: 2,222,222 is roughly ~19 days after the upgrade at typical block times.
	params.DeveloperAccessParams = &types.DeveloperAccessParams{
		UntilBlockHeight: 2_294_222,
		AllowedDeveloperAddresses: []string{
			"gonka10fynmy2npvdvew0vj2288gz8ljfvmjs35lat8n",
			"gonka1v8gk5z7gcv72447yfcd2y8g78qk05yc4f3nk4w",
			"gonka1gndhek2h2y5849wf6tmw6gnw9qn4vysgljed0u",
			"gonka1z66ec2zedwpapp6jrj9raxgl93e5ec9z5my52h",
			"gonka1jw6xg0wun3g8m2fjm8lula82dw5p6jl8yp28mn",
			"gonka15sjedpgseutpnrjx2ge3mgau3s8ft5qzym9waa",
			"gonka1l4a2wtls9rgd2mnnj6mheml5xlq3kknngj4p7h",
			"gonka1f3yg5385n3f9pdw2g3dcjcnfqyej67hcu9vfet",
			"gonka15g5pu70k7l6hvdt8xl80h4mxe332762csupaeg",
			"gonka1uyqp5z3dveamfw4pmw7p7rfvwdvgzewnqrzhsu",
		},
	}

	// Participant access gating:
	// - Block NEW participant registrations until this height (registration opens at this height).
	// - Blocklisted accounts cannot participate in PoC submissions.
	// - Allowlist is disabled by default; can be enabled via governance later.
	// NOTE: 2,222,222 is roughly ~2 weeks after the upgrade at typical block times.
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		NewParticipantRegistrationStartHeight: 2_222_222,
		BlockedParticipantAddresses:           []string{"gonka1blockedxxxxxxxxxxxxxxxxxxxxxx"}, // placeholder; update before the upgrade
		UseParticipantAllowlist:               false,                                           // disabled by default
		ParticipantAllowlistUntilBlockHeight:  0,                                               // no cutoff (stays inactive)
	}

	return k.SetParams(ctx, params)
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper, distrKeeper distrkeeper.Keeper) error {
	sections := []struct {
		name     string
		bounties []BountyReward
	}{
		{"epoch_117", epoch117Rewards},
		{"bounty_program", bountyProgramRewards},
	}

	var totalRequired int64
	for _, section := range sections {
		for _, bounty := range section.bounties {
			totalRequired += bounty.Amount
		}
	}

	feePool, err := distrKeeper.FeePool.Get(ctx)
	if err != nil {
		k.Logger().Warn("failed to get fee pool, skipping bounty distribution", "error", err)
		return nil
	}

	available := feePool.CommunityPool.AmountOf(types.BaseCoin).TruncateInt64()
	if available < totalRequired {
		k.Logger().Warn("insufficient fee pool balance, skipping bounty distribution",
			"required", totalRequired, "available", available)
		return nil
	}

	k.Logger().Info("fee pool balance sufficient", "required", totalRequired, "available", available)

	for _, section := range sections {
		for _, bounty := range section.bounties {
			recipient, err := sdk.AccAddressFromBech32(bounty.Address)
			if err != nil {
				k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
				continue
			}

			coins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(bounty.Amount)))
			if err := distrKeeper.DistributeFromFeePool(ctx, coins, recipient); err != nil {
				k.Logger().Error("failed to distribute bounty", "address", bounty.Address, "error", err)
				continue
			}

			k.Logger().Info("bounty distributed", "section", section.name, "address", bounty.Address, "amount", bounty.Amount)
		}
	}

	return nil
}
